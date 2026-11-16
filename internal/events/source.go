package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// defaultUserAgent identifies the bot to the calendars. It is not a courtesy:
	// the Linux Foundation host answers 403 to the runtime's own
	// "Go-http-client/..." default, which is invisible when testing with curl.
	defaultUserAgent = "cnayp-discord-bot/0.1.0 (+https://github.com/kenesparta/cnayp-discord-bot)"

	// maxResponseBytes caps a response body. The community calendar sends roughly
	// 360 KB for a six month window, most of it pre-rendered markup this package
	// discards, so the ceiling is generous and still bounded.
	maxResponseBytes = 8 << 20

	// requestTimeout bounds one calendar request end to end.
	requestTimeout = 30 * time.Second
)

// A Code says why a calendar failed. The set is closed: it mirrors the error
// handling the requirement defines, and a new one belongs in the requirement
// first.
type Code string

// The ways reading a calendar can fail.
const (
	// CodeUnreachable is a transport failure: DNS, TLS, connection or deadline.
	CodeUnreachable Code = "SOURCE_UNREACHABLE"

	// CodeRejected is a non-2xx response.
	CodeRejected Code = "SOURCE_REJECTED"

	// CodeUnusable is a successful response that is not usable JSON — an HTML
	// page from a renamed route, a body that will not decode, or one past the
	// size ceiling. It is deliberately distinct from CodeEmpty: the community
	// calendar degrades into HTML on a route change, and parsing that as "no
	// events" is exactly the silent failure this package exists to prevent.
	CodeUnusable Code = "SOURCE_UNUSABLE"

	// CodeEmpty is a well formed response carrying no events at all. Neither
	// calendar legitimately empties, so this is a failure rather than a quiet
	// day: an unknown query parameter is ignored rather than rejected upstream,
	// so a renamed filter returns 200 with an empty list forever.
	CodeEmpty Code = "SOURCE_EMPTY"

	// CodeDegraded is a response carrying far fewer events than the last
	// successful one, which catches a partial index or a half-broken filter that
	// CodeEmpty would miss.
	CodeDegraded Code = "SOURCE_DEGRADED"
)

// A SourceError is a failure of one calendar. One calendar failing never fails
// the run: the other still contributes.
type SourceError struct {
	Source Source
	Code   Code

	// Status is the HTTP status for CodeRejected, and zero otherwise.
	Status int

	// Count is the event count for CodeEmpty and CodeDegraded, and zero otherwise.
	Count int

	Err error
}

func (e *SourceError) Error() string {
	switch {
	case e.Status != 0:
		return fmt.Sprintf("%s calendar: %s: status %d", e.Source, e.Code, e.Status)
	case e.Err != nil:
		return fmt.Sprintf("%s calendar: %s: %v", e.Source, e.Code, e.Err)
	default:
		return fmt.Sprintf("%s calendar: %s", e.Source, e.Code)
	}
}

func (e *SourceError) Unwrap() error { return e.Err }

// errBodyTooLarge stops a decode that has read past maxResponseBytes.
var errBodyTooLarge = errors.New("response body exceeds the size limit")

// a capped reader fails instead of truncating, so an oversized body can never
// be mistaken for a short but valid one.
type capped struct {
	r         io.Reader
	remaining int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errBodyTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
}

// A fetcher is the HTTP half of a calendar client: one GET, decoded as JSON,
// with every guard applied before anything is parsed.
type fetcher struct {
	http      *http.Client
	userAgent string
	source    Source
}

// get performs one request and decodes the JSON body into out. Every failure it
// returns is a *SourceError, so the caller can tell an outage from a route that
// started answering HTML.
func (f fetcher) get(ctx context.Context, endpoint string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &SourceError{Source: f.source, Code: CodeUnreachable, Err: withoutURL(err)}
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := f.http.Do(req)
	if err != nil {
		return &SourceError{Source: f.source, Code: CodeUnreachable, Err: withoutURL(err)}
	}
	defer func() {
		// Drain what is left so the connection returns to the pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &SourceError{Source: f.source, Code: CodeRejected, Status: resp.StatusCode}
	}

	// A 200 serving HTML is how the community calendar degrades when a route is
	// renamed, so the content type is checked before the body is touched.
	if mediaType := resp.Header.Get("Content-Type"); !isJSON(mediaType) {
		return &SourceError{
			Source: f.source,
			Code:   CodeUnusable,
			Err:    fmt.Errorf("content type %q is not JSON", mediaType),
		}
	}

	body := &capped{r: resp.Body, remaining: maxResponseBytes}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return &SourceError{Source: f.source, Code: CodeUnusable, Err: err}
	}
	return nil
}

// isJSON reports whether a Content-Type header names JSON, ignoring parameters
// such as "; charset=UTF-8".
func isJSON(mediaType string) bool {
	base, _, _ := strings.Cut(mediaType, ";")
	return strings.EqualFold(strings.TrimSpace(base), "application/json")
}

// withoutURL returns the cause inside a *url.Error. net/http quotes the whole
// request URL in the wrapper's message, which turns every transport error into
// a log line dominated by a query string.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

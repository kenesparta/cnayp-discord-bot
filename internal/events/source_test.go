package events

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testFetcher returns a fetcher pointed at srv, with the production defaults.
func testFetcher(srv *httptest.Server) fetcher {
	return fetcher{http: srv.Client(), userAgent: defaultUserAgent, source: SourceCommunity}
}

func TestFetchSetsAnIdentifyingUserAgent(t *testing.T) {
	// The Linux Foundation host answers 403 to the runtime's default agent
	// string, and does so invisibly: curl works, the bot does not.
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	var out struct{}
	if err := testFetcher(srv).get(t.Context(), srv.URL, &out); err != nil {
		t.Fatalf("get() = %v, want nil", err)
	}

	if got == "" {
		t.Fatal("no User-Agent was sent")
	}
	if strings.Contains(got, "Go-http-client") {
		t.Errorf("User-Agent %q is the runtime default, which upstream rejects", got)
	}
	if !strings.Contains(got, "github.com/kenesparta/cnayp-discord-bot") {
		t.Errorf("User-Agent %q carries no contact address", got)
	}
}

func TestFetchMapsStatusToRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var out struct{}
	err := testFetcher(srv).get(t.Context(), srv.URL, &out)

	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("get() = %v, want *SourceError", err)
	}
	if sourceErr.Code != CodeRejected {
		t.Errorf("Code = %q, want %q", sourceErr.Code, CodeRejected)
	}
	if sourceErr.Status != http.StatusServiceUnavailable {
		t.Errorf("Status = %d, want %d", sourceErr.Status, http.StatusServiceUnavailable)
	}
}

func TestFetchRejectsNonJSONContentType(t *testing.T) {
	// A renamed route on the community calendar answers 200 with a page of
	// markup. Decoding that would fail confusingly; refusing it is the point.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>events</title>")
	}))
	defer srv.Close()

	var out struct{}
	err := testFetcher(srv).get(t.Context(), srv.URL, &out)

	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("get() = %v, want *SourceError", err)
	}
	if sourceErr.Code != CodeUnusable {
		t.Errorf("Code = %q, want %q", sourceErr.Code, CodeUnusable)
	}
}

func TestFetchAcceptsJSONContentTypeWithParameters(t *testing.T) {
	// Both calendars send a charset parameter; only the media type matters.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		_, _ = io.WriteString(w, `{"total":1}`)
	}))
	defer srv.Close()

	var out struct {
		Total int `json:"total"`
	}
	if err := testFetcher(srv).get(t.Context(), srv.URL, &out); err != nil {
		t.Fatalf("get() = %v, want nil", err)
	}
	if out.Total != 1 {
		t.Errorf("Total = %d, want 1", out.Total)
	}
}

func TestFetchRejectsOversizeBody(t *testing.T) {
	chunk := strings.Repeat("a", 64<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"pad":"`)
		// Enough to pass the ceiling; the writes stop failing over once the
		// client has hung up.
		for range 200 {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	var out struct{}
	err := testFetcher(srv).get(t.Context(), srv.URL, &out)

	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("get() = %v, want an error wrapping errBodyTooLarge", err)
	}
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Code != CodeUnusable {
		t.Errorf("get() = %v, want *SourceError with %q", err, CodeUnusable)
	}
}

func TestFetchCappedReaderStopsAtTheLimit(t *testing.T) {
	reader := &capped{r: strings.NewReader(strings.Repeat("x", 100)), remaining: 10}

	read, err := io.ReadAll(reader)
	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("ReadAll() error = %v, want errBodyTooLarge", err)
	}
	if len(read) != 10 {
		t.Errorf("read %d bytes, want 10", len(read))
	}
}

func TestFetchHonoursContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	var out struct{}
	err := testFetcher(srv).get(ctx, srv.URL, &out)

	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("get() = %v, want *SourceError", err)
	}
	if sourceErr.Code != CodeUnreachable {
		t.Errorf("Code = %q, want %q", sourceErr.Code, CodeUnreachable)
	}
}

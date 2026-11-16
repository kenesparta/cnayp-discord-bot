// Package discord is a Discord API client built on the standard library: a REST
// client for the HTTP endpoints the bot needs, and a gateway [Session] that
// keeps a WebSocket connection alive and dispatches events.
//
// It covers slash commands and interactions rather than the whole API, and it
// grows one endpoint at a time as features need them.
package discord

import (
	"bytes"
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
	// defaultBaseURL pins the API version; Discord retires old ones on a schedule.
	defaultBaseURL = "https://discord.com/api/v10"

	// defaultUserAgent is required by Discord and must identify the bot.
	defaultUserAgent = "DiscordBot (https://github.com/kenesparta/cnayp-discord-bot, 0.1.0)"

	// maxRateLimitRetries bounds how often one call waits out a 429.
	maxRateLimitRetries = 3

	// maxErrorBody caps how much of an error response is kept for the message.
	maxErrorBody = 8 << 10
)

// An APIError is a non-2xx response from the REST API.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Code       int    `json:"code"`
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s %s: status %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("%s %s: status %d: %s (code %d)", e.Method, e.Path, e.StatusCode, e.Message, e.Code)
}

// Unauthorized reports whether the token was rejected, which no retry can fix.
func (e *APIError) Unauthorized() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}

// A Client calls the Discord REST API. The zero value is not usable; build one
// with [NewClient]. It is safe for concurrent use.
type Client struct {
	token     string
	baseURL   string
	userAgent string
	http      *http.Client
}

// A ClientOption overrides a [Client] default.
type ClientOption func(*Client)

// WithHTTPClient replaces the underlying HTTP client, for tests or custom transports.
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) { c.http = h }
}

// WithBaseURL points the client at another API root, without a trailing slash.
func WithBaseURL(baseURL string) ClientOption {
	return func(c *Client) { c.baseURL = baseURL }
}

// WithUserAgent sets the User-Agent header Discord requires.
func WithUserAgent(userAgent string) ClientOption {
	return func(c *Client) { c.userAgent = userAgent }
}

// NewClient returns a client authenticating as the bot behind token.
func NewClient(token string, opts ...ClientOption) *Client {
	c := &Client{
		token:     token,
		baseURL:   defaultBaseURL,
		userAgent: defaultUserAgent,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// CurrentApplication reads the application the token belongs to. Its id is
// what [Client.BulkOverwriteCommands] needs; without a gateway session there is
// no READY to learn it from.
func (c *Client) CurrentApplication(ctx context.Context) (Application, error) {
	var application Application
	if err := c.do(ctx, http.MethodGet, "/applications/@me", nil, &application); err != nil {
		return Application{}, err
	}
	if application.ID == "" {
		return Application{}, errors.New("application id is empty")
	}
	return application, nil
}

// BulkOverwriteCommands replaces the bot's registered commands with commands.
// Passing an empty guildID registers them globally, which Discord may take up to
// an hour to propagate; a guild id takes effect immediately.
//
// This is an overwrite, so commands missing from the slice are deleted.
func (c *Client) BulkOverwriteCommands(ctx context.Context, applicationID, guildID string, commands []ApplicationCommand) ([]ApplicationCommand, error) {
	if applicationID == "" {
		return nil, errors.New("application id is empty")
	}

	path := "/applications/" + url.PathEscape(applicationID) + "/commands"
	if guildID != "" {
		path = "/applications/" + url.PathEscape(applicationID) + "/guilds/" + url.PathEscape(guildID) + "/commands"
	}

	// A nil slice would encode as null, which Discord rejects; an empty one clears.
	if commands == nil {
		commands = []ApplicationCommand{}
	}

	var published []ApplicationCommand
	if err := c.do(ctx, http.MethodPut, path, commands, &published); err != nil {
		return nil, err
	}
	return published, nil
}

// CreateMessage posts a message to a channel. It needs the View Channel and
// Send Messages permissions there, and nothing else: the bot never reads the
// channel, so it is never granted Read Message History.
//
// A caller that leaves AllowedMentions nil gets [NoMentions]. Discord's own
// default is the opposite — it resolves every mention in the text — and this
// endpoint exists to publish text the bot did not write.
func (c *Client) CreateMessage(ctx context.Context, channelID string, message Message) error {
	if channelID == "" {
		return errors.New("channel id is empty")
	}
	if message.AllowedMentions == nil {
		message.AllowedMentions = NoMentions()
	}

	path := "/channels/" + url.PathEscape(channelID) + "/messages"
	return c.do(ctx, http.MethodPost, path, message, nil)
}

// RespondToInteraction answers an interaction. Discord accepts exactly one
// response per interaction, within three seconds of receiving it.
func (c *Client) RespondToInteraction(ctx context.Context, interaction *Interaction, response InteractionResponse) error {
	path := "/interactions/" + url.PathEscape(interaction.ID) + "/" + url.PathEscape(interaction.Token) + "/callback"
	return c.do(ctx, http.MethodPost, path, response, nil)
}

// do sends a request, decoding a JSON body into out when out is non-nil, and
// waiting out rate limits up to maxRateLimitRetries times.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode %s %s request: %w", method, redactPath(path), err)
		}
		body = encoded
	}

	for attempt := 0; ; attempt++ {
		retryAfter, err := c.attempt(ctx, method, path, body, out)
		if retryAfter <= 0 || attempt >= maxRateLimitRetries {
			return err
		}

		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// attempt performs one request. A positive return value means the call was rate
// limited and may be retried after that delay.
func (c *Client) attempt(ctx context.Context, method, path string, body []byte, out any) (time.Duration, error) {
	// Errors travel into logs, so they never carry the raw path.
	safePath := redactPath(path)

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, fmt.Errorf("build %s %s request: %w", method, safePath, withoutURL(err))
	}
	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, safePath, withoutURL(err))
	}
	defer func() {
		// Drain before closing so the connection returns to the pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read the body once: both the error message and the rate limit delay come
		// out of it, and it can only be consumed a single time.
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		apiErr := newAPIError(method, safePath, resp.StatusCode, payload)
		if resp.StatusCode == http.StatusTooManyRequests {
			return retryAfter(resp.Header, payload), apiErr
		}
		return 0, apiErr
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		return 0, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return 0, fmt.Errorf("decode %s %s response: %w", method, safePath, err)
	}
	return 0, nil
}

// redacted replaces a credential that must not reach a log.
const redacted = "REDACTED"

// redactPath strips credentials out of a request path so the result is safe to
// log. Two Discord endpoints carry one in the URL itself:
//
//	/interactions/{id}/{token}/callback
//	/webhooks/{application_id}/{token}/...
//
// That token is bearer-equivalent — it authorises posting messages as the bot
// until it expires — and these paths end up in error messages, which the bot
// logs. The surrounding ids are kept: they identify the failed request without
// granting anything.
func redactPath(path string) string {
	segments := strings.Split(path, "/")

	// A leading slash makes segments[0] empty, so the resource is at index 1 and
	// the token, where there is one, at index 3.
	if len(segments) > 3 && (segments[1] == "interactions" || segments[1] == "webhooks") {
		segments[3] = redacted
	}
	return strings.Join(segments, "/")
}

// withoutURL returns the cause inside a [*url.Error], discarding the wrapper.
//
// net/http reports transport failures as a *url.Error whose message quotes the
// entire request URL, which would put the credential back into the log that
// [redactPath] just took out of it. Unwrapping keeps the useful part — the dial
// or timeout error — and every errors.Is target underneath it.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// newAPIError builds an error from a failed response, folding in Discord's JSON
// error body when there is one.
func newAPIError(method, path string, statusCode int, payload []byte) *APIError {
	apiErr := &APIError{Method: method, Path: path, StatusCode: statusCode}
	// A non-JSON body (an edge proxy's HTML error page, say) leaves the status alone.
	_ = json.Unmarshal(payload, apiErr)
	return apiErr
}

// retryAfter reads the rate limit delay, preferring the millisecond precision of
// Discord's JSON body over the whole-second Retry-After header.
func retryAfter(header http.Header, payload []byte) time.Duration {
	var wait struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(payload, &wait) == nil && wait.RetryAfter > 0 {
		return time.Duration(wait.RetryAfter * float64(time.Second))
	}

	if value := header.Get("Retry-After"); value != "" {
		if seconds, err := time.ParseDuration(value + "s"); err == nil && seconds > 0 {
			return seconds
		}
	}
	return time.Second
}

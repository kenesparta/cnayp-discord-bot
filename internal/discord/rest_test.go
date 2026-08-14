package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// recordingServer captures the requests a Client makes and replies with respond.
func recordingServer(t *testing.T, respond http.HandlerFunc) (*Client, *[]*http.Request) {
	t.Helper()

	var requests []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		requests = append(requests, r)
		respond(w, r)
	}))
	t.Cleanup(server.Close)

	return NewClient("test-token", WithBaseURL(server.URL)), &requests
}

func TestClientSendsBotAuthorization(t *testing.T) {
	client, requests := recordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "wss://gateway.invalid"})
	})

	if _, err := client.GatewayURL(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := (*requests)[0]
	if want := "Bot test-token"; req.Header.Get("Authorization") != want {
		t.Errorf("Authorization = %q, want %q", req.Header.Get("Authorization"), want)
	}
	if req.Header.Get("User-Agent") == "" {
		t.Error("User-Agent is empty; Discord requires one")
	}
	if want := "/gateway/bot"; req.URL.Path != want {
		t.Errorf("path = %q, want %q", req.URL.Path, want)
	}
}

func TestGatewayURLRejectsEmptyResponse(t *testing.T) {
	client, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{})
	})

	if _, err := client.GatewayURL(context.Background()); err == nil {
		t.Fatal("GatewayURL() = nil error, want an empty url to be rejected")
	}
}

func TestBulkOverwriteCommandsTargetsTheRightScope(t *testing.T) {
	tests := []struct {
		name     string
		guildID  string
		wantPath string
	}{
		{name: "global", guildID: "", wantPath: "/applications/app-1/commands"},
		{name: "guild scoped", guildID: "guild-9", wantPath: "/applications/app-1/guilds/guild-9/commands"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, requests := recordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode([]ApplicationCommand{{ID: "1", Name: "ping"}})
			})

			published, err := client.BulkOverwriteCommands(context.Background(), "app-1", tt.guildID,
				[]ApplicationCommand{{Name: "ping", Description: "check"}})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(published) != 1 || published[0].Name != "ping" {
				t.Errorf("published = %+v, want the decoded command list", published)
			}

			req := (*requests)[0]
			if req.Method != http.MethodPut {
				t.Errorf("method = %q, want PUT", req.Method)
			}
			if req.URL.Path != tt.wantPath {
				t.Errorf("path = %q, want %q", req.URL.Path, tt.wantPath)
			}
			if want := "application/json"; req.Header.Get("Content-Type") != want {
				t.Errorf("Content-Type = %q, want %q", req.Header.Get("Content-Type"), want)
			}
		})
	}
}

func TestBulkOverwriteCommandsEncodesEmptyListAsArray(t *testing.T) {
	var body string
	client, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte("[]"))
	})

	if _, err := client.BulkOverwriteCommands(context.Background(), "app-1", "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "[]" {
		t.Errorf("request body = %q, want %q; null would be rejected by Discord", body, "[]")
	}
}

func TestBulkOverwriteCommandsRequiresApplicationID(t *testing.T) {
	client := NewClient("test-token")

	if _, err := client.BulkOverwriteCommands(context.Background(), "", "", nil); err == nil {
		t.Fatal("BulkOverwriteCommands() = nil error, want a missing application id to be refused")
	}
}

func TestRespondToInteractionPostsCallback(t *testing.T) {
	var body InteractionResponse
	client, requests := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	})

	interaction := &Interaction{ID: "int-1", Token: "tok-1"}
	if err := client.RespondToInteraction(context.Background(), interaction, MessageReply("pong")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := (*requests)[0]
	if want := "/interactions/int-1/tok-1/callback"; req.URL.Path != want {
		t.Errorf("path = %q, want %q", req.URL.Path, want)
	}
	if body.Type != MessageResponse {
		t.Errorf("response type = %d, want %d", body.Type, MessageResponse)
	}
	if body.Data == nil || body.Data.Content != "pong" {
		t.Errorf("response data = %+v, want the content set", body.Data)
	}
}

func TestClientReportsAPIErrors(t *testing.T) {
	client, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 50035, "message": "401: Unauthorized"})
	})

	err := client.RespondToInteraction(context.Background(), &Interaction{ID: "1", Token: "t"}, MessageReply("x"))

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", apiErr.StatusCode, http.StatusUnauthorized)
	}
	if apiErr.Code != 50035 {
		t.Errorf("code = %d, want 50035", apiErr.Code)
	}
	if !apiErr.Unauthorized() {
		t.Error("Unauthorized() = false, want true for a 401")
	}
	if !strings.Contains(apiErr.Error(), "401: Unauthorized") {
		t.Errorf("Error() = %q, want it to carry the API message", apiErr.Error())
	}
}

func TestClientToleratesNonJSONErrorBody(t *testing.T) {
	client, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>gateway down</html>"))
	})

	err := client.RespondToInteraction(context.Background(), &Interaction{ID: "1", Token: "t"}, MessageReply("x"))

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", apiErr.StatusCode, http.StatusBadGateway)
	}
}

func TestClientRetriesAfterRateLimit(t *testing.T) {
	var calls atomic.Int32
	client, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"retry_after": 0.001, "message": "You are being rate limited."})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.RespondToInteraction(context.Background(), &Interaction{ID: "1", Token: "t"}, MessageReply("x")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if have := calls.Load(); have != 2 {
		t.Errorf("server saw %d calls, want 2 (one rate limited, one successful)", have)
	}
}

func TestClientGivesUpAfterRepeatedRateLimits(t *testing.T) {
	var calls atomic.Int32
	client, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"retry_after": 0.001})
	})

	err := client.RespondToInteraction(context.Background(), &Interaction{ID: "1", Token: "t"}, MessageReply("x"))
	if err == nil {
		t.Fatal("error = nil, want the rate limit to surface once retries run out")
	}
	if want := int32(maxRateLimitRetries + 1); calls.Load() != want {
		t.Errorf("server saw %d calls, want %d", calls.Load(), want)
	}
}

func TestClientHonoursContextCancellation(t *testing.T) {
	client, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"retry_after": 30})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := client.RespondToInteraction(ctx, &Interaction{ID: "1", Token: "t"}, MessageReply("x"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want the deadline to cut the rate limit wait short", err)
	}
}

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		name    string
		header  http.Header
		payload string
		want    time.Duration
	}{
		{
			name:    "json body wins",
			header:  http.Header{"Retry-After": {"5"}},
			payload: `{"retry_after":0.25}`,
			want:    250 * time.Millisecond,
		},
		{
			name:   "falls back to the header",
			header: http.Header{"Retry-After": {"3"}},
			want:   3 * time.Second,
		},
		{
			name: "defaults to a second",
			want: time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if have := retryAfter(tt.header, []byte(tt.payload)); have != tt.want {
				t.Errorf("retryAfter() = %s, want %s", have, tt.want)
			}
		})
	}
}

func TestInteractionDataOption(t *testing.T) {
	data := InteractionData{
		Name: "echo",
		Options: []InteractionDataOption{
			{Name: "text", Type: StringOption, Value: json.RawMessage(`"hello"`)},
			{Name: "count", Type: IntegerOption, Value: json.RawMessage(`3`)},
		},
	}

	option, ok := data.Option("text")
	if !ok {
		t.Fatal("Option(\"text\") = not found, want the option")
	}
	if value, ok := option.StringValue(); !ok || value != "hello" {
		t.Errorf("StringValue() = %q, %t, want %q, true", value, ok, "hello")
	}

	numeric, _ := data.Option("count")
	if _, ok := numeric.StringValue(); ok {
		t.Error("StringValue() on an integer option = ok, want it to report the mismatch")
	}

	if _, ok := data.Option("missing"); ok {
		t.Error("Option(\"missing\") = found, want not found")
	}
}

package bot

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kenesparta/cnayp-discord-bot/internal/command"
	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

const timestamp = "1731758400"

// signedServer serves the interactions handler over commands, and signs
// requests the way Discord does.
type signedServer struct {
	handler http.Handler
	private ed25519.PrivateKey
	logs    *strings.Builder
}

func newSignedServer(t *testing.T, commands []command.Command) *signedServer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	logs := &strings.Builder{}
	handler, err := Interactions(public, commands, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return &signedServer{handler: handler, private: private, logs: logs}
}

func (s *signedServer) send(method, path, body string, sign bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if sign {
		signature := ed25519.Sign(s.private, []byte(timestamp+body))
		req.Header.Set(discord.HeaderSignature, hex.EncodeToString(signature))
		req.Header.Set(discord.HeaderTimestamp, timestamp)
	}
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, req)
	return recorder
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) discord.InteractionResponse {
	t.Helper()
	var response discord.InteractionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, recorder.Body.String())
	}
	return response
}

func commandInteraction(name string) string {
	return `{"id":"1","token":"secret-token","type":2,"data":{"name":"` + name + `"},"member":{"user":{"id":"42","username":"someone"}}}`
}

func TestInteractionsAnswersPingWithPong(t *testing.T) {
	// Discord's own check when the endpoint URL is saved (personal-infra G56).
	s := newSignedServer(t, command.All())
	recorder := s.send(http.MethodPost, "/", `{"type":1}`, true)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := decodeResponse(t, recorder); got.Type != discord.PongResponse {
		t.Errorf("response type = %d, want PONG", got.Type)
	}
	if recorder.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", recorder.Header().Get("Content-Type"))
	}
}

func TestInteractionsRefusesWhatDiscordDidNotSign(t *testing.T) {
	// The second half of Discord's check: a deliberately bad signature must get
	// 401, or Discord refuses the endpoint.
	s := newSignedServer(t, command.All())

	if code := s.send(http.MethodPost, "/", `{"type":1}`, false).Code; code != http.StatusUnauthorized {
		t.Errorf("unsigned: status = %d, want 401", code)
	}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"type":1}`))
	req.Header.Set(discord.HeaderSignature, hex.EncodeToString(ed25519.Sign(s.private, []byte(timestamp+`{"type":2}`))))
	req.Header.Set(discord.HeaderTimestamp, timestamp)
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("signature over another body: status = %d, want 401", recorder.Code)
	}
}

func TestInteractionsRunsTheCommands(t *testing.T) {
	s := newSignedServer(t, command.All())

	ping := decodeResponse(t, s.send(http.MethodPost, "/", commandInteraction("ping"), true))
	if ping.Type != discord.MessageResponse || ping.Data == nil || ping.Data.Content != "pong" {
		t.Errorf("/ping = %+v", ping)
	}

	test := decodeResponse(t, s.send(http.MethodPost, "/", commandInteraction("test"), true))
	if test.Data == nil || test.Data.Flags&discord.FlagEphemeral == 0 {
		t.Errorf("/test is not ephemeral: %+v", test)
	}
}

func TestInteractionsLogsTheCommandNameAndNothingElse(t *testing.T) {
	s := newSignedServer(t, command.All())
	s.send(http.MethodPost, "/", commandInteraction("ping"), true)

	logged := s.logs.String()
	if !strings.Contains(logged, "command=ping") {
		t.Errorf("the command name was not logged:\n%s", logged)
	}
	for _, forbidden := range []string{"secret-token", "someone", "42"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("the logs carry %q, which the privacy policy says they do not:\n%s", forbidden, logged)
		}
	}
}

func TestInteractionsAnswersAnUnknownCommand(t *testing.T) {
	// Discord may still show a command this build dropped, until the next
	// registration. It gets a reply rather than "the application did not respond".
	s := newSignedServer(t, command.All())
	recorder := s.send(http.MethodPost, "/", commandInteraction("retired"), true)

	response := decodeResponse(t, recorder)
	if response.Data == nil || response.Data.Flags&discord.FlagEphemeral == 0 {
		t.Errorf("response = %+v, want an ephemeral note", response)
	}
}

func TestInteractionsReportsAFailingCommand(t *testing.T) {
	failing := command.Command{
		Definition: discord.ApplicationCommand{Name: "broken"},
		Handle: func(context.Context, *discord.Interaction) (discord.InteractionResponse, error) {
			return discord.InteractionResponse{}, errors.New("boom")
		},
	}
	s := newSignedServer(t, []command.Command{failing})

	if code := s.send(http.MethodPost, "/", commandInteraction("broken"), true).Code; code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", code)
	}
	if !strings.Contains(s.logs.String(), "command failed") {
		t.Errorf("the failure was not logged:\n%s", s.logs.String())
	}
}

func TestInteractionsServesOnlyPostToTheRoot(t *testing.T) {
	s := newSignedServer(t, command.All())

	// /events is the Web Adapter's pass-through path. On this function, which
	// anyone can call through its URL, it must mean nothing (personal-infra G60).
	for _, path := range []string{"/events", "/interactions", "/favicon.ico"} {
		if code := s.send(http.MethodPost, path, `{"action":"announce"}`, true).Code; code != http.StatusNotFound {
			t.Errorf("POST %s: status = %d, want 404", path, code)
		}
	}
	if code := s.send(http.MethodGet, "/", "", false).Code; code != http.StatusMethodNotAllowed {
		t.Errorf("GET /: status = %d, want 405", code)
	}
}

func TestInteractionsRefusesAnOversizedBody(t *testing.T) {
	s := newSignedServer(t, command.All())
	body := `{"type":1,"pad":"` + strings.Repeat("x", maxInteractionBytes) + `"}`

	if code := s.send(http.MethodPost, "/", body, true).Code; code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", code)
	}
}

func TestInteractionsRefusesADuplicateCommand(t *testing.T) {
	commands := append(command.All(), command.All()[0])
	if _, err := Interactions(nil, commands, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Error("two commands with one name were accepted")
	}
}

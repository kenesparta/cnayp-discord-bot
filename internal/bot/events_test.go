package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/announce"
	"github.com/kenesparta/cnayp-discord-bot/internal/command"
	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

type fakeRunner struct {
	outcome announce.Outcome
	err     error
	runs    int
}

func (f *fakeRunner) Run(context.Context) (announce.Outcome, error) {
	f.runs++
	return f.outcome, f.err
}

type fakeRegistrar struct {
	count int
	err   error
	calls int
}

func (f *fakeRegistrar) Register(context.Context) (int, error) {
	f.calls++
	return f.count, f.err
}

func postEvent(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return recorder
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestEventsRunsTheAnnouncer(t *testing.T) {
	runner := &fakeRunner{outcome: announce.Outcome{Kind: announce.KindDaily, New: 2, Published: 1}}
	handler := Events(runner, &fakeRegistrar{}, discardLogger())

	recorder := postEvent(handler, "/events", `{"action":"announce"}`)

	if recorder.Code != http.StatusOK || runner.runs != 1 {
		t.Fatalf("status = %d after %d runs", recorder.Code, runner.runs)
	}
	var outcome announce.Outcome
	if err := json.Unmarshal(recorder.Body.Bytes(), &outcome); err != nil || outcome.New != 2 {
		t.Errorf("outcome = %+v, %v", outcome, err)
	}
}

func TestEventsReportsAFailedRun(t *testing.T) {
	handler := Events(&fakeRunner{err: errors.New("load the announcer's records: throttled")}, &fakeRegistrar{}, discardLogger())

	recorder := postEvent(handler, "/events", `{"action":"announce"}`)

	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "throttled") {
		t.Errorf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestEventsRegistersTheCommands(t *testing.T) {
	registrar := &fakeRegistrar{count: 2}
	handler := Events(&fakeRunner{}, registrar, discardLogger())

	recorder := postEvent(handler, "/events", `{"action":"register-commands"}`)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"registered":2`) {
		t.Errorf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestEventsRefusesWhatItDoesNotKnow(t *testing.T) {
	runner, registrar := &fakeRunner{}, &fakeRegistrar{}
	handler := Events(runner, registrar, discardLogger())

	if code := postEvent(handler, "/events", `{"action":"wipe"}`).Code; code != http.StatusBadRequest {
		t.Errorf("unknown action: status = %d", code)
	}
	if code := postEvent(handler, "/events", `not json`).Code; code != http.StatusBadRequest {
		t.Errorf("not JSON: status = %d", code)
	}
	if code := postEvent(handler, "/", `{"action":"announce"}`).Code; code != http.StatusNotFound {
		t.Errorf("POST /: status = %d, want 404", code)
	}
	if runner.runs != 0 || registrar.calls != 0 {
		t.Error("something ran on a request it should have refused")
	}
}

func TestRegistrarPublishesTheWholeSetToTheGuild(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "app-1"})
			return
		}
		var sent []discord.ApplicationCommand
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_ = json.NewEncoder(w).Encode(sent)
	}))
	t.Cleanup(server.Close)

	client := discord.NewClient("token", discord.WithBaseURL(server.URL))
	count, err := NewRegistrar(client, "guild-1", command.All(), discardLogger()).Register(t.Context())

	if err != nil || count != len(command.All()) {
		t.Fatalf("Register = %d, %v", count, err)
	}
	want := []string{"GET /applications/@me", "PUT /applications/app-1/guilds/guild-1/commands"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("requests = %v, want %v", paths, want)
	}
}

func TestServeStopsWhenTheContextEnds(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, addr, http.NotFoundHandler()) }()

	// Wait for it to listen, then ask it to stop.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if conn, err := net.Dial("tcp", addr); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Serve never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v, want nil after a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the context ended")
	}
}

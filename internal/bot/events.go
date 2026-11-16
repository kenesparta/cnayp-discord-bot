package bot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/kenesparta/cnayp-discord-bot/internal/announce"
)

// The actions an event may ask for. The schedule sends announce
// (personal-infra cnayp-bot.tf); CI sends register-commands after a release.
const (
	actionAnnounce         = "announce"
	actionRegisterCommands = "register-commands"
)

// A Runner performs one announcement run. It is [announce.Announcer].
type Runner interface {
	Run(ctx context.Context) (announce.Outcome, error)
}

// A CommandRegistrar publishes the command set. It is [Registrar].
type CommandRegistrar interface {
	Register(ctx context.Context) (int, error)
}

// Events returns the announcer's handler. It serves POST /events and nothing
// else: the Web Adapter's pass-through path, on which every non-HTTP event the
// function is invoked with arrives.
//
// The response is the invocation's result. A failed action answers 500 with the
// error, which is safe to return: [announce.Announcer.Run] fails only before
// anything was published, so repeating it repeats nothing.
func Events(runner Runner, registrar CommandRegistrar, logger *slog.Logger) http.Handler {
	h := &eventsHandler{runner: runner, registrar: registrar, logger: logger}
	mux := http.NewServeMux()
	mux.Handle("POST /events", h)
	return mux
}

type eventsHandler struct {
	runner    Runner
	registrar CommandRegistrar
	logger    *slog.Logger
}

func (h *eventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var event struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&event); err != nil {
		answer(w, http.StatusBadRequest, map[string]string{"error": "the event is not JSON"})
		return
	}

	switch event.Action {
	case actionAnnounce:
		outcome, err := h.runner.Run(r.Context())
		if err != nil {
			h.logger.Error("announcement run failed before publishing", "error", err)
			answer(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		answer(w, http.StatusOK, outcome)
	case actionRegisterCommands:
		count, err := h.registrar.Register(r.Context())
		if err != nil {
			h.logger.Error("command registration failed", "error", err)
			answer(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		answer(w, http.StatusOK, map[string]int{"registered": count})
	default:
		h.logger.Warn("unknown event action", "action", event.Action)
		answer(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
	}
}

func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

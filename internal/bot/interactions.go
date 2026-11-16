package bot

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/kenesparta/cnayp-discord-bot/internal/command"
	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

// maxInteractionBytes caps a request body. An interaction is a few kilobytes;
// anything near this is not one.
const maxInteractionBytes = 64 << 10

// Interactions returns the interactions function's handler. It serves POST / and
// nothing else.
//
// Every request is verified against the application's public key before
// anything reads it. The Function URL is public, so the signature is the only
// thing separating Discord from anyone else who finds it.
func Interactions(publicKey ed25519.PublicKey, commands []command.Command, logger *slog.Logger) (http.Handler, error) {
	handlers, err := registry(commands)
	if err != nil {
		return nil, err
	}
	h := &interactions{publicKey: publicKey, handlers: handlers, logger: logger}
	mux := http.NewServeMux()
	mux.Handle("POST /{$}", h)
	return mux, nil
}

type interactions struct {
	publicKey ed25519.PublicKey
	handlers  map[string]command.Handler
	logger    *slog.Logger
}

func (h *interactions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInteractionBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	signature, timestamp := r.Header.Get(discord.HeaderSignature), r.Header.Get(discord.HeaderTimestamp)
	if discord.VerifyInteraction(h.publicKey, signature, timestamp, body) != nil {
		// Discord itself sends one of these when the endpoint is saved, and
		// refuses an endpoint that does not answer it with 401.
		h.logger.Warn("rejected an interaction request that Discord did not sign")
		http.Error(w, "", http.StatusUnauthorized)
		return
	}

	var interaction discord.Interaction
	if err := json.Unmarshal(body, &interaction); err != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	switch interaction.Type {
	case discord.PingInteraction:
		reply(w, discord.InteractionResponse{Type: discord.PongResponse})
	case discord.ApplicationCommandInteraction:
		h.command(w, r, &interaction)
	default:
		// The bot publishes no components, autocompletion or modals.
		h.logger.Warn("unsupported interaction type", "type", int(interaction.Type))
		http.Error(w, "", http.StatusBadRequest)
	}
}

// command runs one slash command. This is where a command error stops: nothing
// upstream can act on it, so it is logged once and Discord is told it failed.
// The log names the command and nothing else: not its arguments, not who ran it.
func (h *interactions) command(w http.ResponseWriter, r *http.Request, interaction *discord.Interaction) {
	name := interaction.Data.Name
	handle, ok := h.handlers[name]
	if !ok {
		// A command Discord still shows but this build no longer serves, until
		// the next registration reconciles the set.
		h.logger.Warn("unknown command", "command", name)
		reply(w, discord.EphemeralReply("This command is no longer available."))
		return
	}
	response, err := handle(r.Context(), interaction)
	if err != nil {
		h.logger.Error("command failed", "command", name, "error", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	h.logger.Info("command answered", "command", name)
	reply(w, response)
}

func reply(w http.ResponseWriter, response discord.InteractionResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

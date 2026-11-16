// Package bot puts the commands and the announcer behind HTTP, which is how both
// Lambda functions receive their work (personal-infra §5.24):
//
//   - Discord POSTs each interaction to the interactions function's Function
//     URL, signed, and takes the reply from the HTTP response: [Interactions].
//   - The Lambda Web Adapter replays the schedule's event, and the event CI
//     sends to register the commands, as a POST to /events on the announcer,
//     which has no URL at all: [Events].
//
// The two handlers are never served together. /events acts on whatever it is
// sent, so it may only exist on a function nothing outside AWS can call
// (personal-infra G60); that is why the bot is two functions and not one.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/command"
	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

// Serve runs handler on addr until ctx is cancelled, then shuts it down,
// letting a request in flight finish. It blocks, and the goroutine it starts is
// waited for before it returns.
func Serve(ctx context.Context, addr string, handler http.Handler) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// No write timeout: an announcement run takes as long as the calendars
		// do, and the function's own timeout bounds it.
	}
	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := server.Shutdown(shutdown)
		if served := <-served; !errors.Is(served, http.ErrServerClosed) && err == nil {
			err = served
		}
		return err
	}
}

// registry maps command names to their handlers, refusing a definition Discord
// would reject or that would shadow another.
func registry(commands []command.Command) (map[string]command.Handler, error) {
	handlers := make(map[string]command.Handler, len(commands))
	for i, c := range commands {
		if c.Definition.Name == "" {
			return nil, fmt.Errorf("command %d has no name", i)
		}
		if _, duplicate := handlers[c.Definition.Name]; duplicate {
			return nil, fmt.Errorf("command %q is registered twice", c.Definition.Name)
		}
		handlers[c.Definition.Name] = c.Handle
	}
	return handlers, nil
}

// A Registrar publishes the command set to Discord.
type Registrar struct {
	client   *discord.Client
	guildID  string
	commands []command.Command
	logger   *slog.Logger
}

// NewRegistrar returns a registrar publishing commands into guildID, or
// globally when guildID is empty.
func NewRegistrar(client *discord.Client, guildID string, commands []command.Command, logger *slog.Logger) *Registrar {
	return &Registrar{client: client, guildID: guildID, commands: commands, logger: logger}
}

// Register publishes the command set with a bulk overwrite, so Discord is
// reconciled to [command.All], additions and deletions alike, in one call.
// Commands are never registered one by one.
//
// It runs when CI invokes the announcer after a release, because that is the
// function holding the token: there is no gateway READY any more to do it on.
func (r *Registrar) Register(ctx context.Context) (int, error) {
	application, err := r.client.CurrentApplication(ctx)
	if err != nil {
		return 0, fmt.Errorf("read the application: %w", err)
	}
	published, err := r.client.BulkOverwriteCommands(ctx, application.ID, r.guildID, command.Definitions(r.commands))
	if err != nil {
		return 0, fmt.Errorf("publish commands: %w", err)
	}
	scope := r.guildID
	if scope == "" {
		scope = "global"
	}
	r.logger.Info("commands published", "count", len(published), "scope", scope)
	return len(published), nil
}

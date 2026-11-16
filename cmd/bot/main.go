// Command bot runs the cnayp Discord bot as one of its two Lambda functions,
// chosen by BOT_MODE (internal/config).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	// The event calendars name their time zones by IANA id, including legacy
	// aliases such as US/Eastern and Asia/Calcutta, and the binary ships on a
	// scratch-like base image that carries no zone database of its own. Embedding
	// the standard library's copy makes the lookup work the same everywhere
	// rather than only where the base image happens to help.
	_ "time/tzdata"

	"github.com/kenesparta/cnayp-discord-bot/internal/announce"
	"github.com/kenesparta/cnayp-discord-bot/internal/awsapi"
	"github.com/kenesparta/cnayp-discord-bot/internal/bot"
	"github.com/kenesparta/cnayp-discord-bot/internal/command"
	"github.com/kenesparta/cnayp-discord-bot/internal/config"
	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
	"github.com/kenesparta/cnayp-discord-bot/internal/events"
	"github.com/kenesparta/cnayp-discord-bot/internal/state"
)

func main() {
	if err := run(); err != nil {
		slog.Error("bot stopped", "error", err)
		os.Exit(1)
	}
}

// run holds everything main would otherwise do, so that deferred calls run and
// only main decides the exit code.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	handler, err := handlerFor(cfg, logger)
	if err != nil {
		return err
	}

	// Lambda sends SIGTERM when it retires an execution environment.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("bot listening", "mode", cfg.Mode, "addr", cfg.Addr)
	return bot.Serve(ctx, cfg.Addr, handler)
}

// handlerFor builds what the mode serves, and nothing of the other mode: the
// interactions function never sees the token, and never serves /events.
func handlerFor(cfg config.Config, logger *slog.Logger) (http.Handler, error) {
	switch cfg.Mode {
	case config.ModeInteractions:
		handler, err := bot.Interactions(cfg.PublicKey, command.All(), logger)
		if err != nil {
			return nil, fmt.Errorf("build the interactions handler: %w", err)
		}
		return handler, nil
	case config.ModeAnnounce:
		client := discord.NewClient(cfg.DiscordToken)
		var store announce.Store
		if cfg.StateTable != "" {
			store = state.NewTable(awsapi.NewDynamoDB(cfg.Region, cfg.Credentials), cfg.StateTable)
		} else {
			logger.Warn("the announcer's records are in memory: every start is a first run")
			store = state.NewMemory()
		}
		announcer := announce.New(events.New(logger), client, store, cfg.EventsChannelID, logger)
		registrar := bot.NewRegistrar(client, cfg.GuildID, command.All(), logger)
		return bot.Events(announcer, registrar, logger), nil
	default:
		return nil, fmt.Errorf("mode %q has no handler", cfg.Mode)
	}
}

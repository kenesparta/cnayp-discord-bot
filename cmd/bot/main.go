// Command bot runs the cnayp Discord bot.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kenesparta/cnayp-discord-bot/internal/bot"
	"github.com/kenesparta/cnayp-discord-bot/internal/config"
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

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	})))

	b, err := bot.New(cfg, slog.Default())
	if err != nil {
		return fmt.Errorf("create bot: %w", err)
	}

	// SIGTERM is what Docker and Kubernetes send first on shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

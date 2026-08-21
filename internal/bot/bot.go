// Package bot wires a Discord gateway session to the command handlers and owns
// the bot's lifecycle.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/kenesparta/cnayp-discord-bot/internal/announce"
	"github.com/kenesparta/cnayp-discord-bot/internal/command"
	"github.com/kenesparta/cnayp-discord-bot/internal/config"
	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

// Bot serves the commands in [command.All] over a Discord gateway session.
// Create one with [New] and drive it with [Bot.Run].
type Bot struct {
	session  *discord.Session
	logger   *slog.Logger
	guildID  string
	commands []command.Command
	handlers map[string]command.Handler

	// announcer is nil when no announcement channel is configured, which leaves
	// the bot serving commands and nothing else.
	announcer *announce.Announcer
}

// New builds a bot from cfg. It does not touch the network; [Bot.Run] does.
func New(cfg config.Config, logger *slog.Logger) (*Bot, error) {
	commands := command.All()
	handlers := make(map[string]command.Handler, len(commands))
	for _, c := range commands {
		if c.Definition.Name == "" {
			return nil, fmt.Errorf("command %d has no name", len(handlers))
		}
		if _, duplicate := handlers[c.Definition.Name]; duplicate {
			return nil, fmt.Errorf("command %q is registered twice", c.Definition.Name)
		}
		handlers[c.Definition.Name] = c.Handle
	}

	client := discord.NewClient(cfg.DiscordToken)

	// Guilds is all the bot needs to serve slash commands. Widen this only when a
	// feature requires it; message content in particular is a privileged intent
	// that Discord must approve.
	session := discord.NewSession(client, cfg.DiscordToken, discord.IntentGuilds, logger)

	b := &Bot{
		session:  session,
		logger:   logger,
		guildID:  cfg.GuildID,
		commands: commands,
		handlers: handlers,
	}

	if cfg.EventsChannelID != "" {
		b.announcer = announce.New(events.New(logger), client, cfg.EventsChannelID, logger)
	}

	session.OnReady(b.onReady)
	session.OnInteraction(b.onInteraction)
	return b, nil
}

// Run connects to the gateway and serves commands until ctx is cancelled. With
// an announcement channel configured it also runs the announcer alongside the
// session.
//
// The announcer is owned here, and deliberately not started from [Bot.onReady]
// like the command publish is. That handler fires on every fresh READY, and the
// session reconnects with backoff whenever Discord drops it, so an afternoon of
// gateway trouble would become an afternoon of duplicate announcements. Owning
// it here also gives it the ownership every goroutine in this repository has:
// cancelled by the caller's context, and waited for before Run returns.
func (b *Bot) Run(ctx context.Context) error {
	if b.announcer == nil {
		return b.session.Run(ctx)
	}

	// Deferred calls run last to first, so this unwinds as: cancel the context,
	// then wait for the announcer to notice and return.
	runCtx, cancel := context.WithCancel(ctx)
	var running sync.WaitGroup
	defer running.Wait()
	defer cancel()

	running.Add(1)
	go func() {
		defer running.Done()
		b.announcer.Run(runCtx)
	}()

	return b.session.Run(runCtx)
}

// onReady publishes the command set every time a fresh session starts, so
// Discord's view matches [command.All] without any manual step.
func (b *Bot) onReady(ctx context.Context, _ discord.Ready) {
	published, err := b.session.PublishCommands(ctx, b.guildID, command.Definitions(b.commands))
	if err != nil {
		// Publishing is not fatal: previously registered commands keep working, so
		// log it and let the session run rather than dropping the connection.
		b.logger.Error("publish commands", "error", err)
		return
	}

	scope := b.guildID
	if scope == "" {
		scope = "global"
	}
	b.logger.Info("commands published", "count", len(published), "scope", scope)
}

// onInteraction routes an interaction to its command. This is where a command
// error stops: nothing upstream can act on it, so it is logged once.
func (b *Bot) onInteraction(ctx context.Context, i *discord.Interaction) {
	if i.Type != discord.ApplicationCommandInteraction {
		return
	}

	handle, ok := b.handlers[i.Data.Name]
	if !ok {
		b.logger.Warn("unknown command", "command", i.Data.Name)
		return
	}

	if err := handle(ctx, b.session, i); err != nil {
		b.logger.Error("command failed", "command", i.Data.Name, "error", err)
	}
}

// Package command implements the bot's slash commands and exposes the set the
// bot publishes to Discord at startup.
package command

import (
	"context"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

// A Responder is what a handler needs from the gateway session: a way to answer
// the interaction, and the connection health a command may want to report.
type Responder interface {
	Respond(ctx context.Context, i *discord.Interaction, response discord.InteractionResponse) error
	Latency() time.Duration
}

// A Handler answers one slash-command interaction. Discord discards a response
// that arrives more than three seconds after the interaction, so a handler that
// cannot be that fast should reply with a deferred response first.
//
// Returning an error means the interaction went unanswered; the caller logs it.
type Handler func(ctx context.Context, r Responder, i *discord.Interaction) error

// A Command pairs the definition Discord needs in order to publish a slash
// command with the handler invoked when someone runs it.
type Command struct {
	Definition discord.ApplicationCommand
	Handle     Handler
}

// All returns every command the bot publishes. Add new commands here — it is the
// only list the bot reads.
func All() []Command {
	return []Command{ping(), test()}
}

// Definitions returns just the command definitions, in the order All returns them.
func Definitions(commands []Command) []discord.ApplicationCommand {
	definitions := make([]discord.ApplicationCommand, 0, len(commands))
	for _, c := range commands {
		definitions = append(definitions, c.Definition)
	}
	return definitions
}

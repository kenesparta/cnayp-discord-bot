// Package command implements the bot's slash commands and exposes the set the
// bot publishes to Discord at startup.
package command

import (
	"context"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

// A Handler answers one slash-command interaction with the response Discord
// shows. It runs inside the HTTP request Discord made, which Discord abandons
// after three seconds: a handler that cannot be that fast must return a
// deferred response and finish the work some other way.
//
// Returning an error means the interaction goes unanswered; the caller logs it.
type Handler func(ctx context.Context, i *discord.Interaction) (discord.InteractionResponse, error)

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

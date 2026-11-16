package command

import (
	"context"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

// test is a deploy smoke check: it proves that the running build published its
// commands and that interaction routing reaches a handler. The reply is
// ephemeral so checking a deploy leaves nothing behind in the channel, which is
// what separates it from ping — ping reports latency to everyone, this reports
// liveness to the person asking.
func test() Command {
	return Command{
		Definition: discord.ApplicationCommand{
			Type:        discord.ChatInputCommand,
			Name:        "test",
			Description: "Check that the running build answers interactions (only you see the reply)",
		},
		Handle: func(context.Context, *discord.Interaction) (discord.InteractionResponse, error) {
			return discord.EphemeralReply("Test OK — the bot is answering commands."), nil
		},
	}
}

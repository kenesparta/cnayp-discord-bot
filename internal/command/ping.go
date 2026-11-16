package command

import (
	"context"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

// ping is the smallest useful command: it proves the bot answers. There is no
// gateway any more, so there is no heartbeat round trip to report.
func ping() Command {
	return Command{
		Definition: discord.ApplicationCommand{
			Type:        discord.ChatInputCommand,
			Name:        "ping",
			Description: "Check that the bot is alive",
		},
		Handle: func(context.Context, *discord.Interaction) (discord.InteractionResponse, error) {
			return discord.MessageReply("pong"), nil
		},
	}
}

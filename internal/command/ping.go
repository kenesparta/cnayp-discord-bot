package command

import (
	"context"
	"fmt"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

// ping is the smallest useful command: it proves the gateway connection is live
// and reports the heartbeat round trip.
func ping() Command {
	return Command{
		Definition: discord.ApplicationCommand{
			Type:        discord.ChatInputCommand,
			Name:        "ping",
			Description: "Check that the bot is alive and report its gateway latency",
		},
		Handle: func(ctx context.Context, r Responder, i *discord.Interaction) error {
			content := "pong"
			if latency := r.Latency(); latency > 0 {
				content = fmt.Sprintf("pong — gateway latency %s", latency.Round(time.Millisecond))
			}
			return r.Respond(ctx, i, discord.MessageReply(content))
		},
	}
}

# cnayp-discord-bot

A Discord bot in Go with **no external dependencies** — the WebSocket transport (RFC 6455) and the
Discord API client are implemented here on top of the standard library. It currently serves a single
`/ping` slash command and is meant as a base to grow from.

## Requirements

- Go 1.26+
- [sops](https://github.com/getsops/sops) and [age](https://github.com/FiloSottile/age)
- Docker (optional, for local image builds)

## Getting started

1. Create an application at the [Discord Developer Portal](https://discord.com/developers/applications),
   add a bot, and copy its token.
2. Invite it to a server with the `bot` and `applications.commands` scopes.
3. Store the token:

   ```sh
   make secrets-edit    # sets DISCORD_TOKEN in secrets/prod.enc.yaml
   ```

4. Run it:

   ```sh
   make run
   ```

Set `DISCORD_GUILD_ID` to a server id during development: guild-scoped commands appear immediately,
while global ones can take up to an hour to propagate.

## Configuration

| Variable           | Required | Default | Description                                        |
| ------------------ | -------- | ------- | -------------------------------------------------- |
| `DISCORD_TOKEN`    | yes      | —       | Bot token from the developer portal.                |
| `DISCORD_GUILD_ID` | no       | global  | Restrict command registration to one guild.         |
| `LOG_LEVEL`        | no       | `info`  | `debug`, `info`, `warn` or `error`.                 |

## Layout

```
cmd/bot/            main(): config, signals, shutdown
internal/config/    environment parsing and validation
internal/bot/       command registry and interaction routing
internal/command/   one file per slash command; All() is the registry
internal/discord/   Discord REST client and gateway session
internal/websocket/ RFC 6455 client: handshake, framing, masking, close
deployments/        docker compose for running the published image
secrets/            sops-encrypted secrets
```

Each layer depends only on the ones below it: `bot` → `discord` → `websocket`.

## Adding a command

Add a file to `internal/command` returning a `Command`, then add it to `All()` in `command.go`.
Publishing is a bulk overwrite on every connect, so Discord picks up additions and removals on
restart.

```go
func echo() Command {
	return Command{
		Definition: discord.ApplicationCommand{
			Type:        discord.ChatInputCommand,
			Name:        "echo",
			Description: "Repeat a message back",
			Options: []discord.ApplicationCommandOption{{
				Type: discord.StringOption, Name: "text",
				Description: "What to repeat", Required: true,
			}},
		},
		Handle: func(ctx context.Context, r Responder, i *discord.Interaction) error {
			option, _ := i.Data.Option("text")
			text, _ := option.StringValue()
			return r.Respond(ctx, i, discord.MessageReply(text))
		},
	}
}
```

Discord discards a response that arrives more than three seconds after the interaction. A handler
that cannot be that fast should send a `discord.DeferredMessageResponse` first.

## Secrets

Secrets are encrypted with sops + age and never written to disk in the clear —
`sops exec-env` injects them as environment variables for the duration of the process.

```sh
make secrets-edit    # decrypt, edit, re-encrypt
make secrets-show    # print the decrypted contents
```

Your age identity must be readable by sops. On macOS sops looks in
`~/Library/Application Support/sops/age/keys.txt`; if your key lives at `~/.config/sops/age/keys.txt`
export `SOPS_AGE_KEY_FILE` to point at it (the Makefile already does).

To grant access to another maintainer or a deploy host, add their public key to `.sops.yaml` and run
`sops updatekeys secrets/prod.enc.yaml`.

## Deployment

Pushes to `main` publish `ghcr.io/kenesparta/cnayp-discord-bot:edge`; `v*` tags publish semver tags
and `:latest`. On the host:

```sh
sops exec-env secrets/prod.enc.yaml 'docker compose -f deployments/docker-compose.yaml up -d'
```

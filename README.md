# cnayp-discord-bot

A Discord bot in Go with **no external dependencies** — the WebSocket transport (RFC 6455) and the
Discord API client are implemented here on top of the standard library. It currently serves a single
`/ping` slash command and is meant as a base to grow from.

It runs for **Cloud Native Ayacucho — Peru**, a volunteer community that meets to learn about cloud
native technologies, and is used for educational and informational purposes only. It is an
independent project, not endorsed by or affiliated with the CNCF, the Linux Foundation, or Discord.

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

## Policy site

Discord requires a Terms of Service and Privacy Policy URL for the application. The pages live in
[`site/`](site) and are published to S3 behind CloudFront at:

- Terms of Service — https://cnayp-bot.kenesparta.dev/terms.html
- Privacy Policy — https://cnayp-bot.kenesparta.dev/privacy.html

The bot itself serves no HTTP; it only dials out to Discord. The site is a separate static deploy,
run by `.github/workflows/site.yml` on `v*` tags — a legal document goes live because a release was
cut, not because `main` moved. It authenticates to AWS by assuming an IAM role through GitHub's OIDC
provider, so no AWS access keys exist on either side.

**The AWS side is not defined in this repo.** The bucket, CloudFront distribution, DNS records and
deploy role live in the `personal-infra` repository (`terraform/static-cnayp-bot.tf`, spec §5.11).
Get the three values this repo's workflow needs from there:

```sh
terraform output cnayp_bot_site_bucket           # kenesparta-cnayp-bot-site
terraform output cnayp_bot_site_distribution_id  # -> CLOUDFRONT_DISTRIBUTION_ID variable
terraform output cnayp_bot_site_role_arn         # -> AWS_ROLE_ARN secret
```

| Kind | Name | Notes |
| ---- | ---- | ----- |
| Secret | `AWS_ROLE_ARN` | Not a credential; the trust policy is what guards it. |
| Variable | `CLOUDFRONT_DISTRIBUTION_ID` | Required — the deploy fails without it. |

The published set is exactly `index.html`, `terms.html`, `privacy.html`, `404.html` and `style.css`.
`404.html` is **required**: an S3 origin behind OAC answers a missing key with 403, and the
distribution maps both 403 and 404 to `/404.html`. The workflow fails fast if any of them is absent.

Preview locally with any static server, for example:

```sh
python3 -m http.server -d site 8000    # http://localhost:8000/terms.html
```

Both documents describe the bot's actual data handling, which follows from the `GUILDS`-only gateway
intent and the absence of any persistent storage. **Changing either — widening intents, adding a
database or a cache, or logging new fields — means updating the Privacy Policy in the same change.**

## Deployment

Pushes to `main` publish `ghcr.io/kenesparta/cnayp-discord-bot:edge`; `v*` tags publish semver tags
and `:latest`. On the host:

```sh
sops exec-env secrets/prod.enc.yaml 'docker compose -f deployments/docker-compose.yaml up -d'
```

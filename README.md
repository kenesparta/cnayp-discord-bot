# cnayp-discord-bot

A Discord bot in Go with **no external dependencies** — the WebSocket transport (RFC 6455), the
Discord API client and the event calendar clients are implemented here on top of the standard
library. It serves two slash commands — `/ping`, which reports gateway latency to the channel, and
`/test`, an ephemeral deploy smoke check — and, once a day, posts upcoming cloud native events into
one channel, merged from three public calendars: the CNCF community groups, the Kubernetes Community
Days, and the Linux Foundation's conferences.

It runs for the **Cloud Native Ayacucho Community (CNAYP)**, a volunteer community that meets to
learn about cloud native technologies, and is used for educational and informational purposes only.
It is an independent project, not endorsed by or affiliated with the CNCF, the Linux Foundation, or Discord.

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

`DISCORD_GUILD_ID` is set in `secrets/prod.enc.yaml`, so both `make run` and the deployed container
register the commands in the CNAYP guild rather than globally. That is deliberate: the bot serves one
community, and a guild-scoped bulk overwrite takes effect immediately where a global one needs up to
an hour to propagate. Unset it only if the bot is ever meant to serve servers beyond CNAYP.

## Configuration

| Variable           | Required | Default | Description                                        |
| ------------------ | -------- | ------- | -------------------------------------------------- |
| `DISCORD_TOKEN`    | yes      | —       | Bot token from the developer portal.                |
| `DISCORD_GUILD_ID` | no       | global  | Guild to register commands in. Set to the CNAYP guild — see above. |
| `DISCORD_EVENTS_CHANNEL_ID` | no | off | Channel for the daily event announcements. Unset leaves the announcer off. |
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

The bot itself serves no HTTP and has no listener; it dials out to Discord and, once a day, to the
three public event calendars it announces from. The site is a separate static deploy,
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
intent, the absence of any persistent storage, and the fact that the only hosts it talks to are
Discord and the three public event calendars. **Changing any of those — widening intents, adding a
database or a cache, logging new fields, calling a new third-party host, or posting somewhere new —
means updating the Privacy Policy in the same change.**

## Application metadata

What the Developer Portal shows users in the "Add App" dialog. It lives in Discord, not in this repo,
so it is recorded here to keep it reviewable:

> A bot for the Cloud Native Ayacucho Community (CNAYP). Shares learning material and documentation,
> and posts upcoming cloud native events from public CNCF and Linux Foundation calendars into one
> channel on a daily schedule. Educational and informational use only; it cannot read your messages
> and stores nothing.

The portal is edited by hand and CI cannot see it: after changing the text above, paste it into the
Developer Portal before cutting the release.

Tags: `cloud native`, `community`, `kubernetes`, `peru`, `education` (max 5, 20 characters each).

That description repeats the Privacy Policy's two load-bearing claims — no message reading, no
storage — so **the constraint above covers it as well**: widening intents or adding storage
falsifies the portal text too, and nothing in CI will catch that.

The description and tags are settable through the API; the Terms of Service and Privacy Policy URLs
are **not** — `PATCH /applications/@me` ignores them, so those two fields have to be edited by hand
in the portal. To re-apply the description and tags, or to read back what is currently live:

```sh
# read
sops exec-env secrets/prod.enc.yaml \
  'curl -s -H "Authorization: Bot $DISCORD_TOKEN" https://discord.com/api/v10/applications/@me'

# write (body in app.json: {"description": "...", "tags": [...]})
sops exec-env secrets/prod.enc.yaml \
  'curl -s -X PATCH -H "Authorization: Bot $DISCORD_TOKEN" -H "Content-Type: application/json" \
     --data-binary @app.json https://discord.com/api/v10/applications/@me'
```

## Deployment

Pushes to `main` publish `ghcr.io/kenesparta/cnayp-discord-bot:edge`; `v*` tags publish semver tags
and `:latest`. On the host:

```sh
sops exec-env secrets/prod.enc.yaml 'docker compose -f deployments/docker-compose.yaml up -d'
```

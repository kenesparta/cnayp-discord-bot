# cnayp-discord-bot

A Discord bot in Go with **no external dependencies** — the Discord API client, AWS request signing,
a DynamoDB client and the event calendar clients are implemented here on top of the standard library.
It serves two slash commands — `/ping`, which answers `pong`, and `/test`, an ephemeral deploy smoke
check — and, once a day, posts upcoming cloud native events into one channel, merged from three
public calendars: the CNCF community groups, the Kubernetes Community Days, and the Linux
Foundation's conferences.

It runs as two AWS Lambda functions built from one image, chosen by `BOT_MODE`:

- **`interactions`** — Discord delivers each slash command to its Function URL over HTTPS, signed with
  the application's key; the reply goes back in the HTTP response. It holds no secret.
- **`announce`** — EventBridge Scheduler invokes it daily at 13:00 UTC. It keeps what each calendar
  listed in a DynamoDB table, so it can tell what is new, and it registers the slash commands when CI
  asks it to after a release.

There is no gateway connection: nothing in Lambda holds a WebSocket for days.

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

4. Run the announcer locally (records in memory; the secrets carry no announcement channel, so it
   only registers commands when asked):

   ```sh
   make run
   curl -X POST localhost:8080/events -d '{"action":"register-commands"}'   # publishes for real
   ```

`DISCORD_GUILD_ID` is set in `secrets/prod.enc.yaml` and in `personal-infra`, so both `make run` and
the deployed functions register the commands in the CNAYP guild rather than globally. That is deliberate: the bot serves one
community, and a guild-scoped bulk overwrite takes effect immediately where a global one needs up to
an hour to propagate. Unset it only if the bot is ever meant to serve servers beyond CNAYP.

## Configuration

| Variable           | Mode | Required | Default | Description |
| ------------------ | ---- | -------- | ------- | ----------- |
| `BOT_MODE`         | both | yes      | —       | `interactions` or `announce`. |
| `DISCORD_PUBLIC_KEY` | interactions | yes | — | The application's public key (64 hex), from the Developer Portal. Public. |
| `DISCORD_TOKEN`    | announce | yes  | —       | Bot token. On Lambda it is to come from `passkana`, the owner's vault. |
| `DISCORD_GUILD_ID` | announce | no   | global  | Guild to register commands in. Set to the CNAYP guild — see above. |
| `DISCORD_EVENTS_CHANNEL_ID` | announce | no | off | Channel for the daily event announcements. Unset leaves the announcer off. |
| `STATE_TABLE`      | announce | on Lambda, with a channel | memory | DynamoDB table for the announcer's records. |
| `LOG_LEVEL`        | both | no       | `info`  | `debug`, `info`, `warn` or `error`. |

Lambda supplies `AWS_LWA_PORT` (default `8080`), `AWS_REGION` and the execution role's credentials;
`personal-infra` sets the rest.

## Layout

```
cmd/bot/            main(): config, the mode's handler, signals, shutdown
internal/config/    environment parsing and validation, per mode
internal/bot/       the two HTTP handlers (interactions, /events) and command registration
internal/command/   one file per slash command; All() is the registry
internal/announce/  the daily run: what is new, rendering, publishing
internal/events/    the three public event calendars (no Discord in it)
internal/state/     the announcer's records: DynamoDB on Lambda, memory locally
internal/discord/   Discord REST client and interaction signature verification
internal/awsapi/    Signature Version 4 and a minimal DynamoDB client
secrets/            sops-encrypted secrets
```

Each layer depends only on the ones below it.

## Adding a command

Add a file to `internal/command` returning a `Command`, then add it to `All()` in `command.go`.
Publishing is a bulk overwrite on every release, when CI invokes the announcer's
`register-commands`, so Discord picks up additions and removals then.

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
		Handle: func(_ context.Context, i *discord.Interaction) (discord.InteractionResponse, error) {
			option, _ := i.Data.Option("text")
			text, _ := option.StringValue()
			return discord.MessageReply(text), nil
		},
	}
}
```

The handler runs inside Discord's HTTP request, which Discord abandons after three seconds. A handler
that cannot be that fast must return a `discord.DeferredMessageResponse` and finish another way.

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

The bot does not serve these pages. Its interactions function answers only Discord's signed POSTs, and
its announcer has no URL; the site is a separate static deploy,
run by `.github/workflows/site.yml` on `v*` tags — a legal document goes live because a release was
cut, not because `main` moved. It authenticates to AWS by assuming an IAM role through GitHub's OIDC
provider, so no AWS access keys exist on either side.

**The AWS side is not defined in this repo.** The bucket, CloudFront distribution, DNS records and
deploy role live in the `personal-infra` repository (`terraform/static-cnayp-bot.tf`, spec §5.11).
Get the three values this repo's workflow needs from there:

```sh
terraform output cnayp_bot_site_bucket           # kenesparta-cnayp-bot-pages
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

Both documents describe the bot's actual data handling, which follows from what Discord delivers (the
interactions someone runs, and nothing else), what the announcer stores (the identifiers of public
events each calendar listed, and per-calendar counts, never anything about a person), and the fact
that the only hosts it talks to are Discord, the three public event calendars and AWS. **Changing any
of those — receiving more from Discord, storing anything new, logging new fields, calling a new
third-party host, or posting somewhere new — means updating the Privacy Policy in the same change.**

## Application metadata

What the Developer Portal shows users in the "Add App" dialog. It lives in Discord, not in this repo,
so it is recorded here to keep it reviewable:

> A bot for the Cloud Native Ayacucho Community (CNAYP). Shares learning material and documentation,
> and posts upcoming cloud native events from public CNCF and Linux Foundation calendars into one
> channel on a daily schedule. Educational and informational use only; it cannot read your messages
> and stores no personal information.

The portal is edited by hand and CI cannot see it: after changing the text above, paste it into the
Developer Portal before cutting the release.

Tags: `cloud native`, `community`, `kubernetes`, `peru`, `education` (max 5, 20 characters each).

That description repeats the Privacy Policy's two load-bearing claims — no message reading, no
personal information stored — so **the constraint above covers it as well**: receiving more from
Discord or storing anything about a person falsifies the portal text too, and nothing in CI will
catch that.

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

`.github/workflows/release.yml` pushes the image to the private Amazon ECR repository
`cnayp-discord-bot` in `us-east-1`. Pushes to `main` publish `:edge` and `:sha-<short>` and deploy
nothing. A `v*` tag publishes semver tags and `:latest`, then points **both** Lambda functions —
`cnayp_discord_bot` and `cnayp_discord_bot_announcer` — at the pushed digest, and invokes the
announcer with `{"action":"register-commands"}`. Cutting a `v*` tag is what puts a release live.

The functions, the state table, the schedule and the role are Terraform in `personal-infra` (its spec
§5.24). The workflow authenticates by assuming the IAM role `github-actions-ecr-push-cnayp-discord-bot`
through GitHub's OIDC provider. Its trust policy admits only `main` and `v*` tags of this repo; it can
push to that one ECR repository, update those two functions and invoke the announcer, and nothing
else. **One-time setup by the owner:** store the role's ARN as the repository secret
`AWS_ECR_PUSH_ROLE_ARN`, reading it in `personal-infra` with:

```sh
make output NAME=ecr_push_role_arns   # the cnayp-discord-bot role -> AWS_ECR_PUSH_ROLE_ARN secret
```

That is a different secret from the policy site's `AWS_ROLE_ARN`, for a different role, on purpose —
the two stay separate. The registry host is not written anywhere in this repo; the workflow takes it
from `amazon-ecr-login`.

In the Developer Portal, **Interactions Endpoint URL** is the interactions function's URL. Discord
tests it when it is saved — a `PING` that must get `PONG`, and a deliberately bad signature that must
get `401` — so the function has to be live first, and saving it moves delivery off any gateway
connection at once.

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

A Discord bot in Go (module `github.com/kenesparta/cnayp-discord-bot`, Go 1.26.5) with **zero external
dependencies** — the WebSocket protocol and the Discord API client are implemented in this repo on
top of the standard library. It is an intentionally small starting point (one `/ping` command)
designed so features are added by growing `internal/`, not by restructuring.

Keep `go.mod` dependency-free. Adding a third-party module is a deliberate decision, not a default;
prefer writing the small piece that is actually needed (`zen-copy-over-dependency`).

## Git

Commit only with the current user as author. Never add a `Co-Authored-By: Claude ...` trailer or a
"Generated with Claude Code" footer — the commit message ends with its last content line. Same for PR bodies.

## Commands

```sh
make build          # -> bin/bot
make test           # go test -race ./...
make lint           # go vet + golangci-lint (v2 config in .golangci.yml)
make run            # sops exec-env + go run ./cmd/bot
make docker         # local image build
make secrets-edit   # decrypt, open $EDITOR, re-encrypt in place

go test -run '^TestSessionIdentifiesAndBecomesReady$' ./internal/discord   # single test
go test -run '^TestReadFrame$/^masked_server_frame_is_rejected$' ./internal/websocket   # subtest
```

`golangci-lint` is not installed locally; `make lint` fails on the lint step until it is
(`brew install golangci-lint`). CI runs it either way.

## Architecture

Four layers, each depending only on the ones below it. `main` wires and hands off; everything lives
under `internal/` so nothing outside the module can import it.

```
cmd/bot            main(): config → signals → run() → exit code
  └── internal/bot        command registry, interaction routing, publishing
        └── internal/command   one file per slash command; All() is the registry
        └── internal/discord   REST client + gateway session (the Discord protocol)
              └── internal/websocket   RFC 6455 client (the transport)
```

- **`internal/websocket`** — a client-only RFC 6455 implementation: TLS dial, the HTTP Upgrade
  handshake with the `Sec-WebSocket-Accept` check, frame parse/write, client-side masking,
  fragmentation reassembly, automatic pong replies, and close frames surfaced as `*CloseError`.
  It deliberately rejects what it does not support (extensions, compression, masked server frames)
  rather than tolerating it. It knows nothing about Discord.
- **`internal/discord`** — `Client` is the REST side (bot auth, JSON, `*APIError`, 429 retry with
  `retry_after`). `Session` is the gateway side: Hello → Identify/Resume → heartbeat → dispatch,
  with reconnect and backoff.
- **`internal/bot`** — maps command names to handlers and owns what happens on READY.
- **`internal/command`** — `All()` is the single registry; a new command is a new file plus one entry.
- **`internal/config`** — the only place that reads the environment.

### Conventions that matter

- **Concurrency stays with the caller.** `Bot.Run` and `Session.Run` block; they do not spawn
  detached goroutines or return channels. Every goroutine that is started has an owner that cancels
  it and a `WaitGroup` that waits for it before its function returns.
- **`Session.connect` unwinds deliberately.** Its defers are declared so they run as *cancel the
  connection context → wait for the heartbeat and interaction goroutines → close the socket*.
  Reordering those three lines leaks goroutines or closes the socket out from under them.
- **Interaction handlers run on their own goroutine**, tracked by that same `WaitGroup`, so a slow
  command cannot stall the read loop or the heartbeat.
- **Commands are published with a bulk overwrite** on every READY, so Discord is reconciled to
  `command.All()` — additions *and* deletions — in one call. Never register commands individually.
- **`Bot.onInteraction` is the error boundary.** Nothing upstream can act on a command error, so it
  is logged exactly once there and not propagated.
- **A failed publish is not fatal**: previously registered commands keep working, so it logs and the
  session runs on rather than dropping the connection.
- **No credential reaches an error string.** `/interactions/{id}/{token}/callback` and
  `/webhooks/{app_id}/{token}` carry a bearer-equivalent token in the URL, and interaction errors
  get logged. Two things enforce this in `rest.go`: `redactPath` scrubs the token out of the path
  stored on `APIError` and formatted into wrapped errors, and `withoutURL` unwraps `*url.Error`,
  whose message quotes the whole request URL and would otherwise put it straight back. Any new error
  built from a path must go through both. `TestInteractionErrorsNeverCarryTheToken` and
  `TestTransportErrorsNeverCarryTheToken` are the guards.
- **Fatal vs retryable disconnects** are decided by `fatalCloseError`. Close codes 4004/4010–4014
  (bad token, bad intents, bad API version) end `Run`; everything else reconnects with backoff.
  A new non-retryable code goes in that function, nowhere else.

Gateway intents are `IntentGuilds` only (`internal/bot/bot.go`). Widen deliberately — message
content is a privileged intent requiring Discord approval.

### Testing

Protocol code is tested against real sockets, not mocks: `internal/websocket` runs a hand-written
server over `net.Listen` and `net.Pipe`, and `internal/discord` stands up a `fakeGateway` that
speaks the server half of the protocol plus an `httptest` REST stub. The server-side framing in
`gateway_test.go` is intentional duplication — the client codec is unexported and must not be
loosened for tests.

Timing-sensitive gateway tests use short heartbeat intervals; they have been run with `-count=8
-race` clean. If one goes flaky, suspect the goroutine ownership in `connect`, not the timing.

## Policy site

`site/` holds the Terms of Service and Privacy Policy the Discord developer portal links to, served
from S3 behind CloudFront at `cnayp-bot.kenesparta.dev`. **The bot process serves no HTTP at all** —
it only dials out to Discord, has no listener, no `EXPOSE`, and no published ports. The site is an
entirely separate static deploy; do not add an HTTP server to the bot to host these.

The HTML in `site/` is the single source of truth — there is deliberately no Markdown copy, because
two versions of a legal document drift.

The Privacy Policy makes specific factual claims about the bot's behaviour: `GUILDS` intent only, no
persistent storage, and logs that record command *names* but not their argument values, user IDs, or
usernames. Any change to intents, storage, or the fields passed to `logger.*` falsifies one of those
claims — update `site/privacy.html` in the same change.

### The AWS side lives in another repo

The bucket, CloudFront distribution, DNS and deploy role are Terraform in **`personal-infra`**
(`terraform/static-cnayp-bot.tf`, spec §5.11) — not here. Do not add IAM JSON, bucket policies, or
distribution config to this repo; that would create a second source of truth for resources Terraform
owns. This repo only uploads files. The workflow's `AWS_ROLE_ARN` secret and
`CLOUDFRONT_DISTRIBUTION_ID` variable come from `terraform output`.

Three constraints that repo imposes, which `site.yml` depends on:

- **The published set is fixed**: `index.html`, `terms.html`, `privacy.html`, `404.html`, plus
  `style.css`. `404.html` is *required* — an S3 origin behind OAC returns 403 for a missing key
  (the distribution has no `s3:ListBucket`), and both 403 and 404 map to `/404.html`. Without it
  CloudFront serves its own generic error page. A guard step fails the build if any is missing.
- **The role grants `CreateInvalidation` but not `GetInvalidation`.** Never `aws cloudfront wait
  invalidation-completed` — it would fail with AccessDenied.
- **No immutable caching, ever** (spec G19 in reverse). These documents are overwritten in place;
  `immutable` cannot be invalidated out of a browser and would strand readers on a superseded
  policy. Edge TTL is 300s default / 3600s max, and invalidation is mandatory, not optional.

## Secrets (sops + age)

Encrypted at rest in `secrets/*.enc.yaml`; recipients live in `.sops.yaml`. Secrets are never
written to disk in the clear — `sops exec-env` injects them as environment variables.

The age identity is at `~/.config/sops/age/keys.txt`, but sops on macOS defaults to
`~/Library/Application Support/sops/age/keys.txt`. The Makefile exports `SOPS_AGE_KEY_FILE` to
bridge that; outside make, export it yourself or sops will fail to decrypt.

Adding a recipient (new maintainer or deploy host) means editing `.sops.yaml` and running
`sops updatekeys secrets/prod.enc.yaml`.

## Deployment

`.github/workflows/release.yml` builds and pushes to `ghcr.io/kenesparta/cnayp-discord-bot` on
pushes to `main` (`:edge`, `:sha-<short>`) and on `v*` tags (semver + `:latest`). The image is a
static `CGO_ENABLED=0` binary on `distroless/static`, running as `nonroot`.

`deployments/docker-compose.yaml` runs the published image on a host; it expects the environment to
already carry the secrets, i.e. launched through `sops exec-env`.

Two build-file details that bite when the module gains its first dependency:

- The Dockerfile copies `go.mod go.su[m]` — a bracket glob, because `go.sum` does not exist while
  the module has no dependencies. It starts matching by itself; no edit needed.
- `.dockerignore` is allowlist-style. New top-level directories the build needs must be added there
  explicitly, or the build will not see them.

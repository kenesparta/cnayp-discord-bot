# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

A Discord bot in Go (module `github.com/kenesparta/cnayp-discord-bot`, Go 1.26.5) with **zero external
dependencies** — the Discord API client, Ed25519 interaction verification, AWS Signature Version 4 and
a minimal DynamoDB client are implemented in this repo on top of the standard library. It serves two
slash commands (`/ping`, `/test`) and announces upcoming cloud native events once a day, and it is
designed so features are added by growing `internal/`, not by restructuring.

It runs as **two AWS Lambda functions built from one image**, chosen by `BOT_MODE` (personal-infra
spec §5.24, AD-21): `interactions` answers the slash commands Discord POSTs to its Function URL, and
`announce` performs the run EventBridge Scheduler invokes daily at 13:00 UTC. Both sit behind the
Lambda Web Adapter, so the binary is a plain HTTP server. There is no gateway connection.

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
make run            # the announcer locally: sops exec-env + BOT_MODE=announce go run ./cmd/bot
make docker         # local image build
make secrets-edit   # decrypt, open $EDITOR, re-encrypt in place

go test -run '^TestInteractionsAnswersPingWithPong$' ./internal/bot   # single test
go test -run '^TestLoadRefuses$/^a_truncated_key$' ./internal/config   # subtest
```

Run `make lint` before pushing — CI runs golangci-lint v2 and it is stricter than `go vet`. In
particular `revive` requires a doc comment on every exported `const` block (not just on the type
above it) and flags unused function parameters, including in tests, where `_` is the fix.

## Architecture

Layered, each package depending only on the ones below it. `main` wires and hands off; everything
lives under `internal/` so nothing outside the module can import it. `internal/announce` and
`internal/discord` are siblings: the announcer uses the Discord client, and `internal/events` uses
neither — it must never import a Discord type.

```
cmd/bot            main(): config → the mode's handler → bot.Serve → exit code
  └── internal/bot        the two HTTP handlers, command registration, Serve
        └── internal/command   one file per slash command; All() is the registry
        └── internal/state     the announcer's records: DynamoDB on Lambda, memory locally
              └── internal/announce  one run: what is new, rendering, publishing
                    └── internal/events    the three public event calendars (no Discord in it)
        └── internal/discord   REST client, interaction types, Ed25519 verification
        └── internal/awsapi    SigV4 signing, the DynamoDB JSON protocol (no Discord in it)
```

- **`internal/bot`** — `Interactions` serves `POST /` only: it verifies `X-Signature-Ed25519` before
  reading anything, answers PING with PONG, and routes slash commands. `Events` serves `POST /events`
  only — the Web Adapter's pass-through path, where the schedule's `{"action":"announce"}` and CI's
  `{"action":"register-commands"}` arrive. `Registrar` publishes the command set.
- **`internal/announce`** — `Announcer.Run` is one run against a `Store`. The schedule is not here.
- **`internal/state`** — the item design for personal-infra's `cnayp-discord-bot-state` table
  (`calendar#<source>`, `digest#<date>`), documented in `state.go`.
- **`internal/discord`** — `Client` is the REST side (bot auth, JSON, `*APIError`, 429 retry with
  `retry_after`); `VerifyInteraction` checks Discord's signature.
- **`internal/awsapi`** — `Sign` (tested against AWS's published `get-vanilla` vector) and the two
  DynamoDB operations the records need.
- **`internal/config`** — the only place that reads the environment, per mode.

### Conventions that matter

- **The two handlers are never served together.** `/events` acts on whatever it is sent, so it may
  exist only on the announcer, which has no URL; the interactions function, which anyone can call,
  answers 404 there (personal-infra G60). Do not merge the modes into one function.
- **Each mode holds only what it uses.** The interactions function gets the public key and nothing
  secret; only the announcer gets the token and the table.
- **Verify before reading.** Every interaction is checked against the public key before it is
  decoded. Discord tests the endpoint on save with a deliberately bad signature that must get 401.
- **Discord abandons a request after three seconds.** A handler runs inside it and returns its
  response; anything slower returns a deferred response and finishes another way.
- **The announcer saves before it publishes.** `Run` returns an error only while nothing has been
  published (loading or saving the records failed), so a retry repeats nothing; a publishing
  failure is logged, never returned, because a retry would post again what went out.
- **The record is kept per calendar.** A failed or degraded calendar keeps yesterday's record, so its
  recovery announces only what is genuinely new; a calendar with no record is seeded silently.
- **The weekly digest is claimed before it is posted** (`digest#<date>`, a conditional put), because
  the schedule delivers at least once.
- **DynamoDB's TTL is not trusted to have run.** `state.Table` treats an expired record as absent.
- **Concurrency stays with the caller.** `Serve` blocks, and the goroutine it starts is waited for
  before it returns. Every goroutine that is started has an owner that cancels it and waits for it.
- **Commands are published with a bulk overwrite**, so Discord is reconciled to `command.All()` —
  additions *and* deletions — in one call. Never register commands individually. It happens when
  CI invokes the announcer after a release.
- **The interactions handler is the error boundary.** Nothing upstream can act on a command error, so
  it is logged exactly once there, by command name only, and Discord is told the command failed.
- **No credential reaches an error string or a log.** `/interactions/{id}/{token}/callback` and
  `/webhooks/{app_id}/{token}` carry a bearer-equivalent token in the URL. Two things enforce this in
  `rest.go`: `redactPath` scrubs the token out of the path stored on `APIError` and formatted into
  wrapped errors, and `withoutURL` unwraps `*url.Error`, whose message quotes the whole request URL.
  Any new error built from a path must go through both. `TestInteractionErrorsNeverCarryTheToken` and
  `TestTransportErrorsNeverCarryTheToken` are the guards. `awsapi.Credentials` redacts itself in
  every print path, slog included.

### Testing

Protocol code is tested against real HTTP, not mocks of it: `internal/bot` signs requests with a
generated Ed25519 key and serves them through the real handlers; `internal/discord` and
`internal/awsapi` use `httptest` servers. `internal/announce` and `internal/state` use small fakes of
the `Store` and of DynamoDB. `make contract` reaches the real calendars and posts nothing.

## Policy site

`site/` holds the Terms of Service and Privacy Policy the Discord developer portal links to, served
from S3 behind CloudFront at `cnayp-bot.kenesparta.dev`. **The bot does not serve these pages.** Its
interactions function answers only Discord's signed POSTs, and its announcer has no URL. The site is
an entirely separate static deploy; do not add routes to the bot to host these.

The HTML in `site/` is the single source of truth — there is deliberately no Markdown copy, because
two versions of a legal document drift.

Both documents frame the bot as a tool for the **Cloud Native Ayacucho Community (CNAYP)**, used
for *educational and informational purposes only* (`terms.html` §2, and the disclaimers in §9 and
§10 that lean on it). A command that did something outside that scope — anything transactional, commercial, or
advisory — would falsify it; widen §2 in the same change rather than letting the pages describe a
narrower bot than the one that ships.

The same two claims — no message reading, no personal information stored — are also in the Discord application
description, which lives in the Developer Portal rather than in git. It is transcribed in the README
("Application metadata") so a change here has somewhere to check against; the portal itself still has
to be updated by hand, and CI cannot catch a drift.

The Privacy Policy makes specific factual claims about the bot's behaviour: no gateway connection and
no intents, interactions delivered signed over HTTPS, no permission to read message history, a stored
record of public event identifiers and per-calendar counts (and digest dates) that expires after five
weeks and holds nothing about a person, outbound calls to Discord, exactly three public event
calendars and AWS, and logs that record command *names* and run counts but not argument values, user
IDs, usernames, or anything an event's organiser wrote. Any change to what Discord delivers, what
`internal/state` stores, outbound hosts, or the fields passed to `logger.*` falsifies one of those
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

`.github/workflows/release.yml` builds and pushes to the private Amazon ECR repository
`cnayp-discord-bot` (`us-east-1`) on pushes to `main` (`:edge`, `:sha-<short>`) and on `v*` tags
(semver + `:latest`). The image is a static `CGO_ENABLED=0` binary on `distroless/static`, running
as `nonroot`, with the Lambda Web Adapter as an extension (it is statically linked too). **Only a
`v*` tag deploys**: it points both functions at the pushed digest (Terraform ignores `image_uri`,
personal-infra G48), builds with `provenance: false` / `sbom: false` because Lambda refuses an image
index (G49), then invokes the announcer with `{"action":"register-commands"}`.

- **It assumes `github-actions-ecr-push-cnayp-discord-bot` over OIDC via the secret
  `AWS_ECR_PUSH_ROLE_ARN`** — a different role and secret from `site.yml`'s `AWS_ROLE_ARN`. Never
  point both workflows at one role: one role, one repository, one resource. The ECR repository, the
  two functions, the state table, the schedule and the role are all Terraform in `personal-infra`.
- **Never hard-code the registry host** (`<account>.dkr.ecr.us-east-1.amazonaws.com`). The
  workflow takes it from `amazon-ecr-login`'s `registry` output.
- **The token is to come from `passkana`**, the owner's vault (personal-infra §5.23). Its client is
  not integrated yet, so on Lambda the announcer refuses to start without `DISCORD_TOKEN`, and the
  release's command registration fails until it is. The interactions function needs no secret.

Two build-file details that bite when the module gains its first dependency:

- The Dockerfile copies `go.mod go.su[m]` — a bracket glob, because `go.sum` does not exist while
  the module has no dependencies. It starts matching by itself; no edit needed.
- `.dockerignore` is allowlist-style. New top-level directories the build needs must be added there
  explicitly, or the build will not see them.

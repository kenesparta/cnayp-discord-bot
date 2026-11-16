BIN     := bin/bot
# A bare local name, so `make docker` needs no AWS credentials. The published
# image is <account>.dkr.ecr.us-east-1.amazonaws.com/cnayp-discord-bot; pass
# IMAGE=... to tag a local build with that reference instead.
IMAGE   ?= cnayp-discord-bot
SECRETS := secrets/prod.enc.yaml

# sops looks under ~/Library/Application Support on macOS; point it at the XDG
# path instead. Override if your age identity lives somewhere else.
export SOPS_AGE_KEY_FILE ?= $(HOME)/.config/sops/age/keys.txt

.PHONY: build test lint contract tidy run docker secrets-edit secrets-show

build:
	go build -trimpath -o $(BIN) ./cmd/bot

test:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run

# Checks the public event calendars against the assumptions internal/events
# makes about them. It reaches the network, so it is deliberately not part of
# `test`: a failure means an upstream moved, not that the bot broke.
contract:
	go test -count=1 -tags contract -run TestContract -v ./internal/events/

tidy:
	go mod tidy

# Runs the announcer locally, with the decrypted secrets injected as environment
# variables; nothing is ever written to disk in the clear. It serves POST /events
# on :8080, as the Lambda Web Adapter would, and keeps its records in memory.
# The secrets carry no DISCORD_EVENTS_CHANNEL_ID, so the announcer is off, and
#   curl -X POST localhost:8080/events -d '{"action":"register-commands"}'
# publishes the command set to the guild for real.
run:
	sops exec-env $(SECRETS) 'BOT_MODE=announce go run ./cmd/bot'

docker:
	docker build -t $(IMAGE):dev .

secrets-edit:
	sops $(SECRETS)

secrets-show:
	sops --decrypt $(SECRETS)

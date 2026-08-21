BIN     := bin/bot
IMAGE   ?= ghcr.io/kenesparta/cnayp-discord-bot
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

# Runs the bot with the decrypted secrets injected as environment variables;
# nothing is ever written to disk in the clear.
run:
	sops exec-env $(SECRETS) 'go run ./cmd/bot'

docker:
	docker build -t $(IMAGE):dev .

secrets-edit:
	sops $(SECRETS)

secrets-show:
	sops --decrypt $(SECRETS)

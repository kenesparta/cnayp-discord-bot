BIN     := bin/bot
IMAGE   ?= ghcr.io/kenesparta/cnayp-discord-bot
SECRETS := secrets/prod.enc.yaml

# sops looks under ~/Library/Application Support on macOS; point it at the XDG
# path instead. Override if your age identity lives somewhere else.
export SOPS_AGE_KEY_FILE ?= $(HOME)/.config/sops/age/keys.txt

.PHONY: build test lint tidy run docker secrets-edit secrets-show

build:
	go build -trimpath -o $(BIN) ./cmd/bot

test:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run

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

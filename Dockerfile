# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies change far less often than source, so resolve them in their own
# layer. The bracket glob keeps this working while the bot has no dependencies
# and therefore no go.sum; it starts matching on its own once one appears.
COPY go.mod go.su[m] ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bot ./cmd/bot

# distroless/static ships CA certificates and a nonroot user, and nothing else —
# the bot is a static binary that only needs to dial Discord over TLS.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bot /bot
USER nonroot:nonroot
ENTRYPOINT ["/bot"]

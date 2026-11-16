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
# the bot is a static binary that serves HTTP and dials Discord, the calendars
# and DynamoDB over TLS.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bot /bot
# The AWS Lambda Web Adapter, as a Lambda extension (personal-infra AD-18, its
# §5.21). On Lambda it turns each invocation into an HTTP request to the bot on
# AWS_LWA_PORT, and a non-HTTP event, the schedule's, into a POST to /events.
# Off Lambda the extension directory is ignored. It is statically linked, so it
# runs on this base. Pinned by the digest of the multi-platform index; bump the
# version and the digest together.
COPY --from=public.ecr.aws/awsguru/aws-lambda-adapter:1.1.0@sha256:17cfd08eff1dfea3f6a9a1e9c65fdac80aa4919b6085e746615530f43f57d2f1 /lambda-adapter /opt/extensions/lambda-adapter
USER nonroot:nonroot
ENTRYPOINT ["/bot"]

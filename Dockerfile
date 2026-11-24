# Stage 1 — Build
# Pinned to the exact patch go.mod's security pin requires (see the comment
# on the `go` directive there, and GO_VERSION in the Makefile). A floating
# golang:1.26-alpine tag could resolve to 1.26.0–1.26.5, which carry the
# crypto/tls and crypto/x509 advisories that pin exists to exclude.
FROM golang:1.26.6-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod tidy

COPY . .
RUN CGO_ENABLED=0 make build && cp cellrift /cellrift

# Stage 2 — Runtime
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

COPY --from=builder /cellrift /usr/local/bin/cellrift

# Include built-in scenarios.
COPY scenarios/ /scenarios/

ENTRYPOINT ["cellrift"]

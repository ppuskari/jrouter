# This Dockerfile builds the jrouter binary for itself. It's provided as a
# simple way to build a Docker container directly without having to deal with
# mage.
FROM golang:alpine AS builder
WORKDIR /go/src/jrouter
COPY . .
RUN --mount=type=cache,target=/var/cache/apk \
    apk add build-base libpcap-dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 go build -v \
    -ldflags "-extldflags=-static" \
    -o jrouter .

FROM alpine:latest
LABEL maintainer="Josh Deprez <josh.deprez@gmail.com>"
LABEL "org.opencontainers.image.source"="https://gitea.drjosh.dev/josh/jrouter"
COPY --from=builder /go/src/jrouter/jrouter /usr/bin/jrouter
ENTRYPOINT ["/usr/bin/jrouter", "-config", "/etc/jrouter/jrouter.yaml"]

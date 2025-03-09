FROM golang:alpine AS builder
ARG VERSION_SUFFIX='-dev'
WORKDIR /go/src/jrouter
COPY . .
RUN --mount=type=cache,target=/var/cache/apk \
	apk add build-base libpcap-dev
RUN --mount=type=cache,target=/root/.cache/go-build \
	--mount=type=cache,target=/go/pkg/mod \
	CGO_ENABLED=1 CGO_FLAGS='-static' go build -v \
	-ldflags "-X gitea.drjosh.dev/josh/jrouter/meta.Suffix=${VERSION_SUFFIX}" \
	-o jrouter .

FROM alpine:latest
COPY --from=builder /go/src/jrouter/jrouter /usr/bin/
RUN apk add --no-cache libpcap
ENTRYPOINT ["/usr/bin/jrouter", "-config", "/etc/jrouter/jrouter.yaml"]

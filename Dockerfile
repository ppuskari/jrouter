FROM scratch
ARG TARGETARCH
ARG VERSION
LABEL maintainer="Josh Deprez <josh.deprez@gmail.com>"
LABEL "org.opencontainers.image.source"="https://gitea.drjosh.dev/josh/jrouter"
COPY ./dist/jrouter_${VERSION}_linux_${TARGETARCH} /usr/bin/jrouter
ENTRYPOINT ["/usr/bin/jrouter", "-config", "/etc/jrouter/jrouter.yaml"]

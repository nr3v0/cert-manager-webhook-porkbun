# cert-manager-webhook-porkbun
# Clone of https://github.com/Talinx/cert-manager-webhook-porkbun (see NOTICE).

# All stages are Red Hat UBI 10.
#
# No image is digest-pinned: rebuilds pick up the latest UBI 10 base images.
#
# Builder: UBI 10 Go Toolset, tracking :latest (floating tag, not
# digest-pinned, so rebuilds pick up new Go releases). It runs on the build
# host's platform and cross-compiles, so multi-arch builds
# (podman --platform) don't need emulation for the Go build.
FROM --platform=$BUILDPLATFORM registry.access.redhat.com/ubi10/go-toolset:latest AS build_deps

# Use the toolset's Go; don't let the go.mod `toolchain` line trigger a download.
ENV GOTOOLCHAIN=local

# go-toolset runs as UID 1001 with HOME/WORKDIR /opt/app-root/src (GOPATH
# defaults to $HOME/go), so copied files must be owned by that user.
WORKDIR /opt/app-root/src

COPY --chown=1001:0 go.mod go.sum ./

RUN go mod download

FROM build_deps AS build

ARG TARGETOS=linux
ARG TARGETARCH

COPY --chown=1001:0 . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -o webhook -ldflags '-w -s -extldflags "-static"' .

# ubi-micro ships no CA bundle, so take Red Hat's trust store from ubi-minimal
# (arch-independent, hence the build platform).
FROM --platform=$BUILDPLATFORM registry.access.redhat.com/ubi10/ubi-minimal:latest AS ca

# UBI 10 micro: minimal Red Hat base with no package manager.
FROM registry.access.redhat.com/ubi10/ubi-micro:latest

LABEL org.opencontainers.image.title="cert-manager-webhook-porkbun" \
      org.opencontainers.image.description="cert-manager ACME DNS01 webhook for Porkbun. Clone of github.com/Talinx/cert-manager-webhook-porkbun (originally by Brandon Sprague)." \
      org.opencontainers.image.source="https://github.com/nr3v0/cert-manager-webhook-porkbun" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=ca /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem /etc/pki/tls/certs/ca-bundle.crt
COPY --from=build /opt/app-root/src/webhook /usr/local/bin/webhook

# Go's crypto/x509 already looks at this path; set it explicitly anyway.
ENV SSL_CERT_FILE=/etc/pki/tls/certs/ca-bundle.crt

# Non-root. OpenShift's restricted-v2 SCC replaces this with an arbitrary UID.
USER 1001

ENTRYPOINT ["/usr/local/bin/webhook"]

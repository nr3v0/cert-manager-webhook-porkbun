# cert-manager-webhook-porkbun
# Clone of https://github.com/Talinx/cert-manager-webhook-porkbun (see NOTICE).

# The builder runs on the build host's platform and cross-compiles, so
# multi-arch builds (buildx/podman --platform) don't need emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build_deps

WORKDIR /workspace

COPY go.mod .
COPY go.sum .

RUN go mod download

FROM build_deps AS build

ARG TARGETOS=linux
ARG TARGETARCH

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -o webhook -ldflags '-w -s -extldflags "-static"' .

# ubi-micro ships no CA bundle, so take Red Hat's trust store from ubi-minimal
# (arch-independent, hence the build platform).
FROM --platform=$BUILDPLATFORM registry.access.redhat.com/ubi10/ubi-minimal:10.3@sha256:fb2f7f857cc60d88e3fad19da505083a682ca4569d7b55a61f64aaa43641f741 AS ca

# UBI 10 micro: minimal Red Hat base with no package manager.
FROM registry.access.redhat.com/ubi10/ubi-micro:10.3@sha256:2ad0f98cb13160c4773c945ef93f88b25831c7f27cd3780d72a7e78b210ea72b

LABEL org.opencontainers.image.title="cert-manager-webhook-porkbun" \
      org.opencontainers.image.description="cert-manager ACME DNS01 webhook for Porkbun. Clone of github.com/Talinx/cert-manager-webhook-porkbun (originally by Brandon Sprague)." \
      org.opencontainers.image.source="https://github.com/nr3v0/cert-manager-webhook-porkbun" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=ca /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem /etc/pki/tls/certs/ca-bundle.crt
COPY --from=build /workspace/webhook /usr/local/bin/webhook

# Go's crypto/x509 already looks at this path; set it explicitly anyway.
ENV SSL_CERT_FILE=/etc/pki/tls/certs/ca-bundle.crt

# Non-root. OpenShift's restricted-v2 SCC replaces this with an arbitrary UID.
USER 1001

ENTRYPOINT ["/usr/local/bin/webhook"]

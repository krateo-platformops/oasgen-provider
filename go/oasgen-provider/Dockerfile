# Build environment
# -----------------
FROM golang:1.25-trixie AS builder
LABEL stage=builder

ARG DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates openssl git tzdata && \
  update-ca-certificates && \
  rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

COPY main.go main.go
COPY apis/ apis/
COPY internal/ internal/

# Build
# BUILD_VERSION is stamped into the binary so a running pod can say exactly which build it is.
# Without it service.version is empty and the collector falls back to the chart label, which cannot
# attribute an incident to a commit (#127). rest-dynamic-controller already did this; this module did not.
ARG BUILD_VERSION
RUN CGO_ENABLED=0 GO111MODULE=on go build -a -ldflags="-X 'main.Build=${BUILD_VERSION}'" -o /bin/manager main.go && \
    strip /bin/manager

# Deployment environment
# ----------------------
FROM golang:1.25-trixie

ENV GOCACHE='/tmp/.cache'
RUN mkdir -p "$GOCACHE/go-build" && chmod -R 1777 "$GOCACHE"

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/manager /bin/manager

# Run as a non-root user (DS-0002)
USER 65532:65532

ENTRYPOINT ["/bin/manager"]
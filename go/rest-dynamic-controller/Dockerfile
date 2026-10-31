# Build environment
# -----------------
FROM golang:1.26-trixie AS builder
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
#
# Retried because proxy.golang.org fails transiently often enough to break releases. Three distinct
# modules flaked in this repo on 2026-10-30 alone -- golang.org/x/text on a docs PR,
# krateo-platformops/plumbing in 0.43.0's IMAGE BUILD, and go.opentelemetry.io/otel in a test job -- each
# needing a manual re-run. The plumbing one left 0.43.0 with its images unpushed and its charts correctly
# withheld, and the installer's pin gate refused the tag until it was re-run.
#
# The shared workflows now retry this (krateo-platformops/.github#34), but only in GitHub Actions steps.
# A Dockerfile RUN is outside that, so the image build -- the longest and most expensive job in the
# release -- was the one place still unprotected.
#
# A BLIND RETRY IS SAFE HERE IN A WAY IT IS NOT IN A CI GATE. The shared action deliberately retries only
# transient network errors, so a missing module or checksum mismatch still fails on the first attempt and
# pin-ci keeps its meaning. Here there is no gate to weaken: the build either produces a binary from the
# modules go.sum names, or it fails. A genuine failure simply fails three times in a few seconds, and the
# last attempt's output is what surfaces -- so the real error is still the one in the log.
#
# Sleeps are fixed rather than exponential: the observed failures were single-request HTTP/2 stream
# errors that succeeded on the next attempt, not rate limiting.
RUN for i in 1 2 3; do \
      go mod download && break; \
      if [ "$i" = 3 ]; then echo "go mod download failed after 3 attempts" >&2; exit 1; fi; \
      echo "go mod download attempt $i failed; retrying in $((i*5))s" >&2; \
      sleep $((i*5)); \
    done

# COPY apis/ apis/
COPY internal/ internal/
COPY main.go main.go

# Build
ARG BUILD_VERSION
RUN CGO_ENABLED=0 GO111MODULE=on go build -a -ldflags="-X 'main.Build=${BUILD_VERSION}'" -o /bin/controller ./main.go && \
    strip /bin/controller

# Deployment environment
# ----------------------
FROM gcr.io/distroless/static:nonroot

# COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /bin/controller /bin/controller

USER nonroot:nonroot

ENTRYPOINT ["/bin/controller"]
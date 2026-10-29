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
RUN go mod download

COPY main.go main.go
COPY apis/ apis/
COPY cmd/ cmd/
COPY internal/ internal/

# Build
# BUILD_VERSION is stamped into the binary so a running pod can say exactly which build it is.
# Without it service.version is empty and the collector falls back to the chart label, which cannot
# attribute an incident to a commit (#127). rest-dynamic-controller already did this; this module did not.
ARG BUILD_VERSION
RUN CGO_ENABLED=0 GO111MODULE=on go build -a -ldflags="-X 'main.Build=${BUILD_VERSION}'" -o /bin/manager main.go && \
    strip /bin/manager
# oasgen-render ships in the same image so it is always the exact generator the manager next to it runs; the
# chart starts it by overriding the command (render.enabled).
RUN CGO_ENABLED=0 GO111MODULE=on go build -ldflags="-X 'main.Build=${BUILD_VERSION}'" -o /bin/oasgen-render ./cmd/oasgen-render && \
    strip /bin/oasgen-render

# Deployment environment
# ----------------------
# distroless/static, matching rest-dynamic-controller. The runtime stage used to be golang:1.26-trixie,
# which shipped a 331 MB image to run two static CGO_ENABLED=0 binaries -- a whole Go toolchain, apt and
# a shell, none of which anything invokes: our tree contains no os/exec and reads no GOCACHE, and both
# entrypoints are exec form (`command: ["/bin/oasgen-render"]`), so there is nothing for a shell to do.
#
# The size is not only a slow pull. The chart mounts /tmp as an emptyDir backed by the NODE filesystem,
# and the provider needs writable disk there to parse an OAS document at all (os.MkdirTemp,
# "ogen-oas-*"). So the image competes for the same disk the parser needs: a full node disk produced five
# RestDefinitions stuck READY=False with no controllers, surfacing as an unexplained readiness timeout
# several layers from the cause (#271, #277).
#
# The GOCACHE env and its mkdir/chmod are dropped. They arrived with the monorepo fold carrying no
# rationale, nothing reads GOCACHE, and distroless has no shell to run the RUN in regardless.
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/manager /bin/manager
COPY --from=builder /bin/oasgen-render /bin/oasgen-render

# Run as a non-root user (DS-0002). nonroot:nonroot IS 65532:65532 in distroless; named rather than
# numeric to match rest-dynamic-controller.
USER nonroot:nonroot

ENTRYPOINT ["/bin/manager"]
# syntax=docker/dockerfile:1
#
# gemini-media-mcp container image (ghcr.io/mordor-forge/gemini-media-mcp).
#
# stdio (what MCP clients spawn):
#   docker run -i --rm -e GEMINI_API_KEY -v "$HOME/generated_media:/output" \
#     -v gemini-media-state:/state ghcr.io/mordor-forge/gemini-media-mcp
#
# Streamable HTTP (remote/shared use; the token is required off loopback):
#   docker run -d -p 8765:8765 \
#     -e GEMINI_API_KEY \
#     -e GEMINI_MEDIA_TRANSPORT=http -e GEMINI_MEDIA_HTTP_ADDR=0.0.0.0:8765 \
#     -e GEMINI_MEDIA_HTTP_TOKEN="$(openssl rand -hex 32)" \
#     -v "$HOME/generated_media:/output" -v gemini-media-state:/state \
#     ghcr.io/mordor-forge/gemini-media-mcp
#   -> endpoint http://localhost:8765/mcp with header "Authorization: Bearer <token>"
#
# Generated files go to /output, spend ledger and video jobs to /state (keep
# /state on a named volume: with --rm an anonymous one is lost). On Linux,
# add --user "$(id -u):$(id -g)" so files in a bind-mounted /output belong to you.
# A config file can be mounted at /home/nonroot/.config/gemini-media-mcp/config.yaml
# (or anywhere, with -e GEMINI_MEDIA_CONFIG=/path).
#
# Build: docker build --build-arg VERSION=1.0.0 -t gemini-media-mcp .

# Cross-compile on the build machine's architecture: no emulation for multi-arch builds.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=
ARG DATE=
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w \
        -X github.com/mordor-forge/gemini-media-mcp/internal/version.Version=${VERSION} \
        -X github.com/mordor-forge/gemini-media-mcp/internal/version.Commit=${COMMIT} \
        -X github.com/mordor-forge/gemini-media-mcp/internal/version.Date=${DATE}" \
      -o /out/gemini-media-mcp ./cmd/gemini-media-mcp

# Volume mount points (distroless has no shell to create them): owned by the
# default user and world-writable, so `docker run --user "$(id -u):$(id -g)"`
# (files in a bind-mounted /output then belong to you) works too.
RUN mkdir -p /out/rootfs/output /out/rootfs/state && chmod 1777 /out/rootfs/output /out/rootfs/state

# distroless/static: CA certificates, tzdata and a non-root user (65532), nothing else.
FROM gcr.io/distroless/static-debian13:nonroot

ARG VERSION=dev
ARG COMMIT=
ARG DATE=
LABEL io.modelcontextprotocol.server.name="io.github.mordor-forge/gemini-media-mcp" \
      org.opencontainers.image.title="gemini-media-mcp" \
      org.opencontainers.image.description="MCP server for Google generative media: Nano Banana images, Veo and Gemini Omni video, Gemini TTS and Lyria music" \
      org.opencontainers.image.source="https://github.com/mordor-forge/gemini-media-mcp" \
      org.opencontainers.image.url="https://github.com/mordor-forge/gemini-media-mcp" \
      org.opencontainers.image.documentation="https://github.com/mordor-forge/gemini-media-mcp#readme" \
      org.opencontainers.image.vendor="mordor-forge" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"

COPY --from=build --chown=65532:65532 /out/rootfs/ /
COPY --from=build /out/gemini-media-mcp /usr/local/bin/gemini-media-mcp
COPY LICENSE /usr/share/doc/gemini-media-mcp/LICENSE

ENV MEDIA_OUTPUT_DIR=/output \
    GEMINI_MEDIA_STATE_DIR=/state
VOLUME ["/output", "/state"]
USER nonroot:nonroot
# Only used with GEMINI_MEDIA_TRANSPORT=http.
EXPOSE 8765

ENTRYPOINT ["gemini-media-mcp"]

# Garrison supervisor image — three stages.
#
#   build          — compiles the supervisor binary (CGO_ENABLED=0).
#   claude-install — downloads the pinned Claude Code binary from
#                    downloads.claude.ai and verifies it against the
#                    GPG-signed release manifest. Runs entirely in the
#                    build environment; nothing from this stage except
#                    the verified binary is copied into the runtime image.
#   <runtime>      — alpine:3.20 with musl dependencies for the native
#                    Claude binary, the supervisor, and the MCP state dir.
#
# Runtime image size is expected in the 280–400 MB range, dominated by the
# ~232 MB Claude binary (per its manifest.size field).

# --- build stage ----------------------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${VERSION}" -o /out/supervisor ./cmd/supervisor

# --- claude install stage -------------------------------------------------
FROM alpine:3.20 AS claude-install

# Pin the Claude Code version. The M2 spike ran on 2.1.117; M2.1 inherits
# that pin so every spike observation applies to the runtime image. Bumping
# this is a dedicated change with a retro note.
ARG CLAUDE_VERSION=2.1.117
ARG CLAUDE_RELEASES=https://downloads.claude.ai/claude-code-releases
ARG CLAUDE_KEY_URL=https://downloads.claude.ai/keys/claude-code.asc
# Anthropic's Claude Code release signing key fingerprint as published at
# https://code.claude.com/docs/en/setup#verify-the-manifest-signature.
# Embedded with spaces so the docs-visual form is preserved; verification
# strips whitespace before comparing.
ARG CLAUDE_KEY_FINGERPRINT="31DD DE24 DDFA B679 F42D  7BD2 BAA9 29FF 1A7E CACE"
ARG CLAUDE_PLATFORM=linux-x64-musl

RUN apk add --no-cache ca-certificates gnupg curl jq \
 && curl -fsSL "${CLAUDE_KEY_URL}" | gpg --import 2>&1 \
 && EXPECTED_FP=$(echo "${CLAUDE_KEY_FINGERPRINT}" | tr -d ' \t') \
 && ACTUAL_FP=$(gpg --fingerprint security@anthropic.com | tr -d ' \t\n') \
 && (echo "${ACTUAL_FP}" | grep -q "${EXPECTED_FP}" \
     || (echo "claude signing key fingerprint mismatch"; echo "expected: ${EXPECTED_FP}"; echo "actual:   ${ACTUAL_FP}"; exit 1)) \
 && mkdir -p /work && cd /work \
 && curl -fsSLO "${CLAUDE_RELEASES}/${CLAUDE_VERSION}/manifest.json" \
 && curl -fsSLO "${CLAUDE_RELEASES}/${CLAUDE_VERSION}/manifest.json.sig" \
 && gpg --verify manifest.json.sig manifest.json \
 && BIN_NAME=$(jq -r ".platforms[\"${CLAUDE_PLATFORM}\"].binary" manifest.json) \
 && BIN_SHA=$(jq -r ".platforms[\"${CLAUDE_PLATFORM}\"].checksum" manifest.json) \
 && test -n "${BIN_NAME}" && test "${BIN_NAME}" != "null" \
 && test -n "${BIN_SHA}"  && test "${BIN_SHA}"  != "null" \
 && curl -fsSL "${CLAUDE_RELEASES}/${CLAUDE_VERSION}/${CLAUDE_PLATFORM}/${BIN_NAME}" -o claude \
 && echo "${BIN_SHA}  claude" | sha256sum -c \
 && chmod +x claude

# --- runtime stage --------------------------------------------------------
# Alpine 3.20 inherited from M1. M1 retro §5 flagged the real-claude step as
# the trigger for a runtime-base review; this Dockerfile is that review. The
# Claude Code setup docs specify libgcc, libstdc++, and ripgrep as required
# packages on musl distros, plus USE_BUILTIN_RIPGREP=0 in the environment.
FROM alpine:3.20
# M2.2: docker-cli is the supervisor's handle for talking to the
# garrison-docker-proxy sidecar (filtered docker socket). Used to exec
# into garrison-mempalace for palace bootstrap, wake-up, hygiene palace
# queries, and for Claude subprocesses spawning mempalace.mcp_server.
RUN apk add --no-cache ca-certificates libgcc libstdc++ ripgrep docker-cli
COPY --from=build /out/supervisor /supervisor
COPY --from=claude-install /work/claude /usr/local/bin/claude
ENV USE_BUILTIN_RIPGREP=0 \
    DISABLE_AUTOUPDATER=1 \
    GARRISON_CLAUDE_BIN=/usr/local/bin/claude \
    GARRISON_MCP_CONFIG_DIR=/var/lib/garrison/mcp
RUN mkdir -p /var/lib/garrison/mcp && chmod 0750 /var/lib/garrison/mcp
ENTRYPOINT ["/supervisor"]

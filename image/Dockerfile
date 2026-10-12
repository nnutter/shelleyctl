# syntax=docker/dockerfile:1
# Debian bookworm-slim multi-platform index (amd64 and arm64).
FROM docker.io/library/debian@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Release and checksums from primary upstream sources:
# https://github.com/boldsoftware/shelley/releases/tag/v0.1363.920203221
# https://github.com/boldsoftware/shelley/releases/download/v0.1363.920203221/checksums.txt
# Use the target architecture, not the build machine's architecture.
ARG TARGETARCH
# Keep Debian utilities eligible for security updates at rebuild time.
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash ca-certificates chromium curl ffmpeg fonts-liberation \
        git gosu iproute2 jq less openssh-client procps ripgrep \
        tini tmux unzip xz-utils \
    && rm -rf /var/lib/apt/lists/* \
    && arch="${TARGETARCH:-$(dpkg --print-architecture)}" \
    && case "$arch" in \
        amd64) checksum=dd6ee2d144727eaf9ad78a13f68b06f8f76ad42df9e327a9af2350dc7be27afc ;; \
        arm64) checksum=0c9bbf9f8b8880cc17b56d92205ac062776ae9999b1950358a57efe349cac7ff ;; \
        *) echo "Unsupported architecture: $arch" >&2; exit 1 ;; \
    esac \
    && curl --fail --location --retry 3 \
        "https://github.com/boldsoftware/shelley/releases/download/v0.1363.920203221/shelley_linux_${arch}" \
        --output /usr/local/bin/shelley \
    && printf '%s  /usr/local/bin/shelley\n' "$checksum" | sha256sum --check --strict \
    && chmod 0755 /usr/local/bin/shelley \
    && groupadd --gid 1000 shelley \
    && useradd --uid 1000 --gid shelley --create-home --shell /bin/bash shelley

ENV HOME=/home/shelley \
    USER=shelley \
    LOGNAME=shelley \
    XDG_CONFIG_HOME=/home/shelley/.config \
    XDG_CACHE_HOME=/home/shelley/.cache \
    XDG_DATA_HOME=/home/shelley/.local/share \
    TMPDIR=/home/shelley/.cache/tmp

# This release hard-codes these paths. Preserve them across container replacement.
# See claudetool/browse/{browse,screencast}.go and claudetool/llm_one_shot.go.
RUN for name in screenshots uploads downloads console-logs oneshot-images screencasts; do \
        ln -s "/home/shelley/.local/share/shelley/artifacts/$name" "/tmp/shelley-$name"; \
    done

COPY --chmod=0755 entrypoint.sh /usr/local/bin/shelley-entrypoint
WORKDIR /home/shelley
EXPOSE 9000

# Root is used only to initialize a fresh root-owned bind mount. The entrypoint
# drops privileges before initializing files or starting Shelley. No sudo or
# container-engine client/socket is provided to the agent.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/shelley-entrypoint"]

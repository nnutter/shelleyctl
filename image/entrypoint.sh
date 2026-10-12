#!/bin/sh
set -eu

# Keep database, config, workspaces, caches and tool artifacts on the home mount.
if [ "${HOME:-}" != /home/shelley ]; then
    echo 'HOME must be /home/shelley. Mount the instance directory there.' >&2
    exit 1
fi

if [ "$(id -u)" = 0 ]; then
    # Do not recursively change ownership of user files or follow a home symlink.
    if [ -L "$HOME" ]; then
        echo 'The mounted home must be a directory, not a symbolic link.' >&2
        exit 1
    fi
    mkdir -p "$HOME"
    chown --no-dereference shelley:shelley "$HOME"
    exec gosu shelley /usr/local/bin/shelley-entrypoint "$@"
fi

if [ ! -w "$HOME" ]; then
    echo 'The mounted home must be writable by the container user (default UID 1000).' >&2
    exit 1
fi

umask 077
mkdir -p "$HOME/.config" "$HOME/.cache/tmp" "$HOME/.local/share/shelley/artifacts"
for name in screenshots uploads downloads console-logs oneshot-images screencasts; do
    mkdir -p "$HOME/.local/share/shelley/artifacts/$name"
done
cd "$HOME"

# Allow an explicit diagnostic command without bypassing home initialization.
if [ "$#" -gt 0 ]; then
    exec "$@"
fi

# Global flags precede the command. Config is optional and instance-owned.
# Upstream net.Listen("tcp", ":"+port) listens on all interfaces, including IPv4.
# Source: cmd/shelley/main.go at v0.1363.920203221.
# VirtioFS home mounts cannot reliably host Unix sockets across replacement.
# The socket is transient; local tools reach it inside the same container.
exec shelley --db "$HOME/shelley.db" --config "$HOME/shelley.json" serve --port 9000 --socket /tmp/shelley.sock

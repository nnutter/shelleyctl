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

# Follow Shelley's user-guidance precedence without hiding existing guidance.
guidance="$HOME/.config/AGENTS.md"
for candidate in "$HOME/.config/AGENTS.md" "$HOME/.config/shelley/AGENTS.md" "$HOME/.agents/AGENTS.md" "$HOME/.shelley/AGENTS.md"; do
    if [ -f "$candidate" ]; then
        guidance=$candidate
        break
    fi
done
if ! grep -Fqx '<!-- shelleyctl:hosted-services-guidance -->' "$guidance" 2>/dev/null; then
    printf '\n' >> "$guidance"
    cat /usr/local/share/shelleyctl/user-guidance.md >> "$guidance"
fi

# Apple VirtioFS uses the runtime process UID for its mount mapping. Run as the
# Mac owner, and supply a libc user record when that UID is absent in the image.
# No root access or ownership change on the host home is needed.
if ! getent passwd "$(id -u)" >/dev/null; then
    passwd_file="$TMPDIR/passwd"
    group_file="$TMPDIR/group"
    awk -F: -v uid="$(id -u)" -v gid="$(id -g)" '
        BEGIN { OFS=":" }
        $1 == "shelley" { $3=uid; $4=gid }
        { print }
    ' /etc/passwd > "$passwd_file"
    cp /etc/group "$group_file"
    if ! getent group "$(id -g)" >/dev/null; then
        printf 'shelley-host:x:%s:\n' "$(id -g)" >> "$group_file"
    fi
    for wrapper in /usr/lib/*/libnss_wrapper.so; do
        if [ -f "$wrapper" ]; then
            export NSS_WRAPPER_PASSWD="$passwd_file" NSS_WRAPPER_GROUP="$group_file"
            export LD_PRELOAD="$wrapper${LD_PRELOAD:+:$LD_PRELOAD}"
            break
        fi
    done
    if ! getent passwd "$(id -u)" >/dev/null; then
        echo 'Cannot initialize the container user identity.' >&2
        exit 1
    fi
fi

# Shelley stores terminal sockets alongside its database. Redirect that runtime
# directory to local storage because Apple VirtioFS cannot host Unix sockets.
# Preserve files from older images rather than deleting terminal logs or state.
terminal_dir=/tmp/shelley-terminals
mkdir -p "$terminal_dir"
chmod 0700 "$terminal_dir"
if [ -L "$HOME/terminals" ]; then
    if [ "$(readlink "$HOME/terminals")" != "$terminal_dir" ]; then
        echo 'The terminals symlink must point to /tmp/shelley-terminals.' >&2
        exit 1
    fi
else
    if [ -e "$HOME/terminals" ]; then
        backup_dir=$(mktemp -d "$HOME/terminals-retained.XXXXXX")
        mv "$HOME/terminals" "$backup_dir/terminals"
    fi
    ln -s "$terminal_dir" "$HOME/terminals"
fi

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

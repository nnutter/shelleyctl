#!/bin/sh
# Native terminal regression check: <image> [podman|container].
set -eu
image=${1:-localhost/shelleyctl:local}
runtime=${2:-podman}
case "$runtime" in
    podman) identities='1000:1000 501:20' ;;
    container) identities="$(id -u):$(id -g)" ;;
    *) echo 'Runtime must be podman or container.' >&2; exit 1 ;;
esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM

for identity in $identities; do
    uid=${identity%:*}
    gid=${identity#*:}
    home="$work/home-$uid"
    mkdir -p "$home/terminals"
    printf 'retained terminal log\n' > "$home/terminals/previous.log"
    # A second container exercises initialization with an existing home link
    # whose transient target disappeared with the first container.
    for attempt in 1 2; do
        set -- "$runtime" run --rm --tty --user "$identity"
        if [ "$runtime" = podman ]; then
            set -- "$@" --pull=never --passwd=false --userns="keep-id:uid=$uid,gid=$gid"
        fi
        # Expand HOME inside the container, not on the host.
        # shellcheck disable=SC2016
        if ! "$@" --volume "$home:/home/shelley" "$image" sh -ec '
            timeout --foreground --kill-after=2s 10s shelley exe-scroll "$HOME/terminals/smoke.sock" -- sh -ec '\''
                test -S "$HOME/terminals/smoke.sock"
                printf "terminal started\n" > "$HOME/terminal-marker"
            '\''
        ' > "$work/run-$uid-$attempt.log" 2>&1; then
            tail -n 10 "$work/run-$uid-$attempt.log" >&2
            exit 1
        fi
        test -s "$home/terminal-marker"
        rm "$home/terminal-marker"
        set -- "$home"/terminals-retained.*/terminals/previous.log
        test "$#" = 1
        grep -qx 'retained terminal log' "$1"
    done
    printf 'Terminal startup, retained logs, and replacement passed for UID %s\n' "$uid"
done

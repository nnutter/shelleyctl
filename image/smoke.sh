#!/bin/sh
# Opt-in native image contract test. Build localhost/shelleyctl:local first.
set -eu
image=${1:-localhost/shelleyctl:local}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM

# Exercise the normal Linux identity and the numeric Mac identity independently.
# keep-id keeps test files host-owned, including for the nonstandard guest UID.
for identity in 1000:1000 501:20; do
    uid=${identity%:*}
    gid=${identity#*:}
    home="$work/home-$uid"
    mkdir "$home"
    # Disable Podman's automatic passwd injection to reproduce Apple container.
    podman run --rm --pull=never --passwd=false \
        --userns="keep-id:uid=$uid,gid=$gid" --user "$identity" \
        --volume "$home:/home/shelley" "$image" sh -ec '
            test "$(id -un)" = shelley
            timeout 10s ssh -G localhost >/dev/null 2>&1
            printf "retained\n" > "$HOME/marker"
            # Linux production uses UID 1000. The ARM64 Mac browser is
            # validated on Studio, not by this amd64 namespace simulation.
            if [ "$(id -u)" = 1000 ]; then
                timeout 20s chromium --headless --no-sandbox --disable-gpu \
                    --dump-dom about:blank > "$HOME/browser.html" 2>/dev/null
                grep -q "<html>" "$HOME/browser.html"
                ffmpeg -version >/dev/null
            fi
        '
    test -s "$home/marker"
    printf 'Named identity, SSH, and host-owned home passed for UID %s\n' "$uid"
done

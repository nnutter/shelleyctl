#!/bin/bash
# Supervise both services: failure or termination of either stops the container.
set -euo pipefail
pids=()
cleanup() {
    trap - EXIT
    if ((${#pids[@]})); then
        kill "${pids[@]}" 2>/dev/null || true
        wait "${pids[@]}" 2>/dev/null || true
    fi
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
shelley --db "$HOME/.local/state/shelley/shelley.db" --config "$HOME/shelley.json" serve --port 9001 --socket /tmp/shelley.sock &
pids+=("$!")
nginx -e stderr -c /etc/shelleyctl/nginx.conf -g 'daemon off;' &
pids+=("$!")
wait -n "${pids[@]}"

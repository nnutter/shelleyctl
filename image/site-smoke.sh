#!/bin/sh
# Native app-routing contract check: <image> [podman|container].
set -eu
image=${1:-localhost/shelleyctl:local}
runtime=${2:-podman}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir "$work/home"
case "$runtime" in
    podman) set -- podman run --rm --pull=never --passwd=false --userns=keep-id:uid=1000,gid=1000 --user 1000:1000 ;;
    container) set -- container run --rm --user "$(id -u):$(id -g)" ;;
    *) echo 'Runtime must be podman or container.' >&2; exit 1 ;;
esac
# Variables in this script belong to the container and to Nginx.
# shellcheck disable=SC2016
if ! "$@" --volume "$work/home:/home/shelley" "$image" timeout --foreground --kill-after=2s 60s sh -ec '
    /usr/local/bin/shelley-services &
    services=$!
    trap '\''kill "$services" 2>/dev/null || true; wait "$services" 2>/dev/null || true'\'' EXIT
    shelley skill cat shelley-site > /tmp/site-skill
    grep -Fq "shelley-site add" /tmp/site-skill
    # A user skill in another supported location must not be shadowed.
    rm "$HOME/.config/shelley/shelley-site"
    mkdir -p "$HOME/.agents/skills/shelley-site"
    cat > "$HOME/.agents/skills/shelley-site/SKILL.md" <<"EOF"
---
name: shelley-site
description: Use the custom family site instructions.
---
Keep the custom family site instructions.
EOF
    /usr/local/bin/shelley-entrypoint shelley skill cat shelley-site > /tmp/custom-site-skill
    grep -Fq "Keep the custom family site instructions." /tmp/custom-site-skill
    test ! -e "$HOME/.config/shelley/shelley-site"
    cat > /tmp/test-app.conf <<"EOF"
pid /tmp/test-app.pid;
error_log /dev/stderr;
events { worker_connections 32; }
http {
    access_log off;
    client_body_temp_path /tmp/shelley-nginx/client;
    proxy_temp_path /tmp/shelley-nginx/proxy;
    fastcgi_temp_path /tmp/shelley-nginx/fastcgi;
    uwsgi_temp_path /tmp/shelley-nginx/uwsgi;
    scgi_temp_path /tmp/shelley-nginx/scgi;
    server {
        listen 18080;
        location = /redirect { return 302 /level; }
        location / { return 200 "$uri$is_args$args|$http_x_forwarded_prefix"; }
    }
}
EOF
    nginx -e stderr -c /tmp/test-app.conf
    i=0
    until curl -fsS --max-time 1 http://127.0.0.1:9000/ >/dev/null 2>&1; do
        i=$((i+1)); test "$i" -lt 100; sleep 0.1
    done
    test -f "$HOME/.local/state/shelley/shelley.db" || {
        echo "Database is not in the hidden state directory." >&2; exit 1
    }
    for path in "$HOME"/shelley.db* "$HOME/terminals" "$HOME"/terminals-retained.*; do
        test ! -e "$path"
        test ! -L "$path"
    done
    shelley-site add game 18080
    i=0
    until test "$(curl -fsS --max-time 1 "http://127.0.0.1:9000/sites/game/level?x=1")" = "/level?x=1|/sites/game"; do
        i=$((i+1)); test "$i" -lt 100; sleep 0.1
    done
    curl -sS -D /tmp/headers http://127.0.0.1:9000/sites/game -o /dev/null
    grep -q "Location: /sites/game/" /tmp/headers
    i=0
    until curl -sS -D /tmp/headers http://127.0.0.1:9000/sites/game/redirect -o /dev/null && grep -q "Location: /sites/game/level" /tmp/headers; do
        i=$((i+1)); test "$i" -lt 100; sleep 0.1
    done
    test "$(shelley-site list)" = game
    if shelley-site add game 9001; then exit 1; fi
    if shelley-site add ../escape 18080; then exit 1; fi
    printf "invalid nginx directive;\n" > "$HOME/.config/shelley/sites/broken.conf"
    if shelley-site add game 18081; then exit 1; fi
    rm "$HOME/.config/shelley/sites/broken.conf"
    nginx -e stderr -s reload -c /etc/shelleyctl/nginx.conf
    sleep 0.5
    test "$(curl -fsS "http://127.0.0.1:9000/sites/game/level?x=1")" = "/level?x=1|/sites/game"
    shelley-site remove game
    i=0
    until test "$(curl -sS -o /dev/null -w "%{http_code}" http://127.0.0.1:9000/sites/game/)" = 404; do
        i=$((i+1)); test "$i" -lt 100; sleep 0.1
    done
    curl -fsS http://127.0.0.1:9000/ >/dev/null
    nginx -e stderr -s stop -c /etc/shelleyctl/nginx.conf
    wait "$services"
    if curl -fsS --max-time 1 http://127.0.0.1:9001/ >/dev/null 2>&1; then exit 1; fi
' > "$work/run.log" 2>&1; then
    tail -n 25 "$work/run.log" >&2
    exit 1
fi
printf 'Shelley root, app paths, redirects, rejection, rollback, and removal passed\n'

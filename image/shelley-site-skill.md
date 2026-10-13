---
name: shelley-site
description: Use when creating, publishing, updating, troubleshooting, or removing a web app or game served at /sites/<name>/ on this Shelley instance.
---

# Publish an app with shelley-site

Use `shelley-site` to publish an app through this instance's existing HTTPS service.
Do not create another Tailscale service, publish another host port, or edit the main Nginx configuration.

## Publish

1. Select a name with at most 40 lowercase letters, digits, or hyphens.
   The name must start with a letter.
2. Select an available port from 1024 through 65535.
   Do not use reserved ports 9000 or 9001.
3. Configure the app's public base path as `/sites/<name>/`, including the trailing slash.
   Configure asset URLs, client-side routing, and cookie paths for that prefix.
4. Start the app on `127.0.0.1:<port>` inside this container.
   Keep the process running in a persistent terminal or a tmux session.
5. Check the app locally with `curl http://127.0.0.1:<port>/`.
6. Register the route:

```sh
shelley-site add game 3000
```

7. Check the public path through Nginx:

```sh
curl -i http://127.0.0.1:9000/sites/game/
```

8. Give the user `<instance-HTTPS-origin>/sites/game/`.
   Use the known instance origin from the user's context.
   Do not guess the tailnet domain or contact an `exe.xyz` service to discover it.

The proxy strips `/sites/game/` before forwarding to the app.
It sends `X-Forwarded-Prefix: /sites/game` and supports WebSocket upgrades.
The app must still use the public prefix for browser URLs.

## Manage and troubleshoot

```sh
shelley-site list
# Change the backend port by registering the same name again:
shelley-site add game 3001
# Remove the route without stopping the app or deleting its files:
shelley-site remove game
```

For a 502 response, check the app process and its loopback port first.
For missing assets or broken navigation, check the app's public base path.
Use the helper for route changes so it can validate, reload, and restore configuration after a failure.
Routes persist under `~/.config/shelley/sites`, but app processes stop during container replacement.
Restart the app after replacement.
Do not claim that registration installs an app launcher.

## Security boundary

Publish only apps that the instance owner trusts.
Apps share Shelley's browser origin and can make requests to its API.
Site routes inherit the instance's Tailscale access policy and are not separate authorization boundaries.
Do not describe a site prefix as a sandbox or use it to host untrusted third-party content.

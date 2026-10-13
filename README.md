# shelleyctl

Manage one persistent [Shelley](https://shelley.dev/) container per family member.
Use rootless Podman on Linux or Apple's `container` CLI on macOS.
The existing host-side Tailscale daemon exposes each instance through its own [Tailscale Service](https://tailscale.com/docs/features/tailscale-services).
There are no Tailscale companion containers.

## Architecture

```text
Family member
  → https://shelley-<name>.<tailnet>.ts.net:443
  → host Tailscale Serve
  → host 127.0.0.1:<stable backend port>
  → instance container on a dedicated network
  → persistent home directory and shelley.db
```

The image includes pinned Shelley, Chromium, ffmpeg, Bash, Git, curl, tmux, and standard utilities.
The Debian base is pinned to a multi-platform digest.
Debian packages receive the versions available when the image is built.
Shelley runs without root privileges.
Containers do not receive the host container socket or host Tailscale state.

## Prerequisites

- A tagged host running a current Tailscale client, with MagicDNS and HTTPS enabled.
- Permission to configure host-side Tailscale Serve.
- Linux: rootless Podman, its normal signature policy, and a working systemd user manager.
- macOS: Apple silicon, Apple's `container` CLI, its Linux kernel, and a logged-in GUI session.
- Go 1.27.2 or later to build the CLI from source.
- Provider credentials or a model endpoint configured in Shelley before using the agent.

The deployment host can use `tag:shelley-host`.
Tagging changes the device's identity and can change its permissions.
Preserve administrative access, including SSH, when changing the tailnet policy.

### Linux setup

Use Podman's distribution instructions to configure rootless operation, subordinate IDs, and its signature policy.
Enable lingering for the account that owns the instances so its user manager starts at boot:

```sh
loginctl enable-linger "$USER"
```

This can require administrator approval.
Without lingering, startup is tied to the user's login session rather than system boot.

Some Homebrew Podman installations do not find their installed policy automatically.
Select the existing trusted policy explicitly in that case:

```sh
export SHELLEYCTL_SIGNATURE_POLICY="$(brew --prefix)/etc/containers/policy.json"
```

`shelleyctl` never generates a permissive image policy.

### macOS setup

Start Apple's container services and install their recommended Linux kernel:

```sh
container system start
container system kernel set --recommended
```

Approve macOS Local Network permission for the container runtime when prompted.
Without approval, a published loopback port can accept connections but fail to forward them.
Startup occurs after GUI login, not before login.
The launcher captures the installation-time PATH, so `container` must be available there.

## Install and build

Install the CLI at a stable path before creating instances.
Do not use `go run` for creation because the generated launcher needs a persistent executable.

```sh
go install ./cmd/shelleyctl
shelleyctl build
```

`build` uses an embedded build context, so the installed binary does not need the source checkout.
The default image tag is `localhost/shelleyctl:local`.
Use `--image <reference>` to build a different tag or select the image for a new instance.
Rebuilding does not replace existing containers.

## Tailscale administration

For instance `alice`:

1. Define service `shelley-alice` in the Tailscale admin console, with a TCP endpoint on port 443.
2. Grant Alice access to `svc:shelley-alice` on TCP port 443.
3. Run `shelleyctl create alice` on the deployment host.
4. Approve that host as a host for the service when it appears in the console.
5. Run `shelleyctl status` to obtain the service URL.

An example grant is:

```json
{
  "grants": [
    {
      "src": ["alice@example.com"],
      "dst": ["svc:shelley-alice"],
      "ip": ["tcp:443"]
    }
  ]
}
```

This is a policy fragment, not a replacement for your whole policy.
Tailscale permissions are additive.
Remove or narrow existing broad grants that also permit access to these services.
Keep the host tag's outbound permissions narrow, because container egress can traverse the host's network.
Do not grant the host blanket access to every family service.

Service definition, host approval, and client authorization are separate actions.
`shelleyctl` manages only the local service endpoint; it does not change the admin console or access policy.
A locally configured endpoint does not prove that the service is approved or reachable by a particular client.
Test both an allowed family client and a denied family client.

## Commands

```sh
shelleyctl status
shelleyctl build
shelleyctl create alice
shelleyctl delete alice
```

Names start with a lowercase letter, end with a letter or digit, and contain at most 40 lowercase letters, digits, or hyphens.
The container and network are named `shelleyctl-<name>`.
The service is named `svc:shelley-<name>`.
Backend ports are allocated from 19000–29999 and retained in the instance record.

Interactive terminals show cancellable Bubble Tea progress.
Use `--plain` to disable interactive progress.
Failures return a nonzero exit status in both modes.

### Shell completion

Generate completion scripts with `shelleyctl completion <shell>`.
Supported shells are Bash, Zsh, Fish, and PowerShell.
Use `shelleyctl completion <shell> --help` for installation instructions.
Generating completion scripts does not read instance state or invoke lifecycle operations.

### Creation and recovery

`create` records desired state before creating external resources.
It ensures a dedicated network and container, starts the backend, checks HTTP readiness, installs startup, and configures the service.
It refuses foreign or conflicting resources rather than replacing them.

If a step fails, correct the reported problem and repeat the same command.
The existing home, image reference, and backend port remain stable.
For example, define a missing Tailscale Service, then repeat `create`.
Manual service approval can remain pending after local configuration succeeds.

Inspect native runtime logs when Shelley does not become ready:

```sh
# Linux
podman logs shelleyctl-alice

# macOS
container logs shelleyctl-alice
```

### Deletion and retention

`delete` records that the instance should be absent before cleanup.
It withdraws the service endpoint, disables startup, and removes the container and its empty owned network.
Independent execution cleanup still runs if withdrawing exposure fails.
A failed deletion must be retried; it is not reported as complete.

Deletion retains:

- The home directory, database, projects, and configuration.
- The instance record, backend port, and image reference.
- The Tailscale Service definition and policy in the admin console.

Repeating `delete` for a known deleted instance is safe.
Creating that name again reuses its retained data and configuration.
Scheduled startup cannot revive a deleted instance.
Unknown names fail without attempting to remove external resources.
There is no automatic permanent-data deletion or upgrade command.

## Data and models

Default state locations are:

| Platform | State directory |
| --- | --- |
| Linux | `$XDG_STATE_HOME/shelleyctl`, or `~/.local/state/shelleyctl` |
| macOS | `~/Library/Application Support/shelleyctl` |

Use `--state-dir /absolute/path` or `SHELLEYCTL_STATE_DIR` to choose another location.
Use one state directory per host, and use it consistently across commands.
Launchers retain the state directory used during creation.

Each instance's `instances/<name>/home` directory mounts at `/home/shelley`.
Its database is `/home/shelley/shelley.db`, and its optional configuration is `/home/shelley/shelley.json`.
Tool artifacts and caches also live in the persistent home.
The local CLI Unix socket is transient at `/tmp/shelley.sock`, outside macOS VirtioFS storage.
Terminal runtime files also use local storage: `/home/shelley/terminals` links to `/tmp/shelley-terminals`.
Terminal sessions do not survive container replacement.
On first startup with the updated image, existing terminal files are retained under `terminals-retained.*/terminals` in the persistent home.
On macOS, the entrypoint supplies libc user identity data for the mount owner's UID so tools such as OpenSSH work.
For native diagnostic commands that need this environment, invoke them through the entrypoint:

```sh
container exec shelleyctl-alice /usr/local/bin/shelley-entrypoint ssh -G localhost
```

Configure models and keys through Shelley's UI; those settings persist in its database.
See [Models and API keys](https://shelley.dev/docs/models) and [shelley.json](https://shelley.dev/docs/config).
The host's provider environment variables are not automatically passed into containers.
Treat the database and home as sensitive data.

Persistence is not a backup.
For a simple consistent backup, delete the instance to stop it and disable scheduled restart, copy its retained home, then create it again.
Back up `state.json` as well.
Do not copy a live SQLite database file by itself while ignoring its WAL.

## Startup and security boundaries

A systemd user service and timer on Linux, or a launchd LaunchAgent on macOS, resumes desired instances approximately every 30 seconds.
The CLI remains the single lifecycle owner; startup does not independently create containers.
This avoids competing creation logic in Quadlet and the CLI.
Installed startup files are reported separately from container state.
Their presence alone does not prove that a session manager is currently running.

The host administrator can access every backend and home directory.
A container can execute arbitrary commands and make outbound network requests.
Containers and private networks reduce accidental cross-instance access; they are not a complete sandbox against hostile users or kernel exploits.
Do not mount additional host resources without reviewing the exposure.

## Development and validation

```sh
mise run all
go test -race -shuffle=on ./...

# Optional native Podman image checks, after building the image:
image/smoke.sh
image/terminal-smoke.sh

# Native terminal regression check on macOS, after building the image:
image/terminal-smoke.sh localhost/shelleyctl:local container
```

Packages separate command presentation, instance recovery, runtime protocols, startup, Tailscale integration, and image construction.
Tests exercise failure recovery and native CLI protocols without requiring a live tailnet.

Real-host validation on Studio confirmed ARM64 image builds, HTTPS service access, repeated creation and deletion, retained home/database files, headless Chromium, and launchd recovery after stopping a container.
On Studio, separate-network IPv4 and IPv6 access to the backend was blocked, with a successful IPv6 backend control.
Linux native runtime creation, repeated removal, database retention, private-network separation, and protection of the published loopback port also passed.
OpenSSH user lookup passed with both the normal Linux UID and the Mac UID.
A real logout/reboot and a denied-family-client policy test remain deployment acceptance checks.
A paid-model conversation invoking Shelley's browser tool was not tested; Chromium itself was exercised on both architectures.

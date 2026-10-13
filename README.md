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
There is no automatic permanent-data deletion.

## Update an existing instance

Install the current `shelleyctl` binary first, because its image build context is embedded.
Build the image, then explicitly replace the instance's container:

```sh
shelleyctl build
shelleyctl update christy
# To explicitly select a different already-built or pulled image:
shelleyctl update christy --to-image localhost/shelleyctl:new
```

Without `--to-image`, update retains the instance's saved image reference and uses its current local contents.
For a custom saved tag, build that tag with `shelleyctl --image <saved-tag> build` first.
Update does not download the latest upstream Shelley release: the bundled release remains pinned in the Dockerfile.
It replaces the container and owned network, but retains the home, database, model configuration, backend port, service definition, and launcher.
Existing sessions and app processes stop during the replacement.
Back up the home before updating if you need to recover from an upstream database migration.
The replacement image must already exist locally, and ownership checks run before removal.
Update refreshes the launcher to the current CLI executable before replacing the container, so scheduled recovery understands the update phases.
If update fails, repeat the same update command or `create NAME` to resume.
Scheduled reconciliation also resumes pending updates without repeatedly removing an already-created replacement.
Delete cancels a pending update and prevents scheduled revival.


## Publish web apps

The image runs Nginx on the published container port 9000 and Shelley internally on port 9001.
Shelley's normal UI, streaming responses, and terminals remain at the root URL.
Apps share the instance's existing Tailscale service and access policy.
The image also installs a discoverable `shelley-site` agent skill with publishing, troubleshooting, and security instructions.
Shelley can load it with `shelley skill cat shelley-site`.
The default skill links from `~/.config/shelley/shelley-site` to the image's current version.
Initialization preserves existing user skills in Shelley's supported search paths.
To customize the installed default, replace that home-directory symlink with your own skill directory.

From a Shelley terminal, start the app on a loopback port, then register it:

```sh
# Run the app on 127.0.0.1:3000, then:
shelley-site add game 3000
shelley-site list
# Remove the route without stopping the app:
shelley-site remove game
```

The app is available at `https://shelley-christy.tail98ced6.ts.net/sites/game/`.
The proxy strips `/sites/game/` before forwarding and sends `X-Forwarded-Prefix` to the app.
Configure the app's public base path as `/sites/game/` so assets and browser-side routes work.
WebSockets are supported, and unknown site paths return 404 instead of reaching Shelley.
Ports 9000 and 9001 are reserved.
Routes persist in `~/.config/shelley/sites`, but the helper does not supervise app processes.
Use an app launcher or a persistent terminal and restart the app after container replacement.
Treat published apps as trusted: they share Shelley's origin and can make requests to its API.
This is not a security boundary between an app and its own Shelley instance.

## Muse audio transcription

The image includes `shelley-transcribe` for recorded-audio transcription through Meta Model API instead of OpenAI.
Store your Meta API key in `~/.config/shelley/muse-api-key` with mode 0600, or select an existing file with `MUSE_API_KEY_FILE`.
Do not paste the key into a conversation.

```sh
shelley-transcribe /home/shelley/recording.webm > /home/shelley/transcript.txt
```

The helper converts the recording to mono 24 kHz PCM WAV and rejects audio longer than ten minutes before contacting Meta.
It sends audio only when explicitly invoked, and it does not retry potentially billed requests automatically.
The API key stays out of process arguments and helper output.
The instance user can read the key file, so this is not administrator-only credential storage.
No transcription request falls back to OpenAI or exe.dev.

This helper does **not** replace Shelley's built-in recording UI or fix **Start voice conversation**.
Muse supplies speech-to-text, while that button uses a different live-conversation and delegation protocol.
See [Muse voice integration findings](docs/muse-voice.md) for the remaining integration work and provider limitations.

## User guidance

The image appends self-hosting guidance to the existing user-level `AGENTS.md`, using Shelley's normal location precedence.
It creates `~/.config/AGENTS.md` when no user guidance exists.
The guidance tells the agent not to use `exe.xyz` services and to use configured providers and local tools instead.
Initialization preserves existing instructions and does not duplicate the guidance on subsequent runs.
This is user-editable model guidance, not a network restriction or a protected administrator policy.
It does not disable UI features that independently contact exe.dev services.

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
Its database and SQLite sidecars are under `/home/shelley/.local/state/shelley/`.
Its optional configuration remains `/home/shelley/shelley.json`.
Tool artifacts and caches also live in the persistent home.
The local CLI Unix socket is transient at `/tmp/shelley.sock`, outside macOS VirtioFS storage.
Terminal runtime files also use local storage: `~/.local/state/shelley/terminals` links to `/tmp/shelley-terminals`.
Terminal sessions do not survive container replacement.
Fresh instances create no database or terminal-state files in the home root.
There is no automatic migration from the old home-root layout during early development.
For a clean recreation, delete the instance, then explicitly back up or remove its retained home before creating it again.
The delete command itself does not remove that home.
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
image/site-smoke.sh
# Builds a temporary embedded-context image and uses a local Meta protocol provider.
# No paid Meta API request is made:
SHELLEYCTL_TEST_IMAGE=localhost/shelleyctl:local go test ./image -run TestNative

# Native terminal regression check on macOS, after building the image:
image/terminal-smoke.sh localhost/shelleyctl:local container
image/site-smoke.sh localhost/shelleyctl:local container
SHELLEYCTL_TEST_IMAGE=localhost/shelleyctl:local go test ./image -run TestNativeEmbeddedContext
```

Packages separate command presentation, instance recovery, runtime protocols, startup, Tailscale integration, and image construction.
Tests exercise failure recovery and native CLI protocols without requiring a live tailnet.

Real-host validation on Studio confirmed ARM64 image builds, HTTPS service access, repeated creation and deletion, retained home/database files, headless Chromium, and launchd recovery after stopping a container.
On Studio, separate-network IPv4 and IPv6 access to the backend was blocked, with a successful IPv6 backend control.
Linux native runtime creation, repeated removal, database retention, private-network separation, and protection of the published loopback port also passed.
OpenSSH user lookup passed with both the normal Linux UID and the Mac UID.
A real logout/reboot and a denied-family-client policy test remain deployment acceptance checks.
A paid-model conversation invoking Shelley's browser tool was not tested; Chromium itself was exercised on both architectures.

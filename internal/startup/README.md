# Startup launchers

The runtime adapter is the only owner of container lifecycle operations.
Startup launchers invoke `EXECUTABLE --plain create NAME` instead of invoking a container runtime directly.
They set `SHELLEYCTL_STATE_DIR`, `SHELLEYCTL_RESUME=1`, and the installer's current `PATH`.
The CLI must interpret resume as reconciliation of active desired records only.
It must not revive a deleted record.
Resume invocations must not call `Ensure`, because launcher replacement can stop the invocation that performs it.
Only an explicit create request installs or repairs the launcher.
Image configuration comes from the persisted record, not the launcher environment.

## Linux

The adapter installs a user service and timer under `$XDG_CONFIG_HOME/systemd/user`, or `~/.config/systemd/user`.
The timer invokes a oneshot service when the user manager starts and 30 seconds after the previous CLI invocation finishes.
An already-running invocation does not overlap another timer activation.
The service uses `/usr/bin/env --` to pass the executable as an argument because systemd restricts characters in executable paths.
The `:` execution prefix disables systemd environment substitution, and doubled percent signs prevent specifier substitution in persisted values.

User managers normally start at login.
Boot without login requires an administrator or user to enable lingering separately with `loginctl enable-linger USER`.
The adapter does not change lingering, root configuration, or system units.
Runtime readiness failures remain retryable through subsequent timer activations.

Quadlet would introduce another container lifecycle owner.
Its generated units could create, replace, or remove the retained container that the runtime adapter already owns.
A CLI reconciliation timer avoids that ownership conflict.
It also preserves the same behavior across Linux and macOS.

## macOS

The adapter installs a LaunchAgent under `~/Library/LaunchAgents` with `RunAtLoad` and `StartInterval=30`.
It uses modern `launchctl enable`, `bootstrap`, `disable`, and `bootout` commands.
Reinstallation keeps a loaded launcher running when its durable definition matches the requested definition.
A changed definition unloads the loaded job before publishing and loading the replacement.
If unloading fails, the old durable definition remains so the next installation attempt retries the update.
A failed bootstrap leaves the replacement file available for another installation attempt.

The target domain is `gui/UID`, which identifies the user's GUI login session.
SSH alone does not guarantee that this domain exists.
Installation and removal first check domain availability and return an actionable error if no GUI session exists.
Installation leaves the durable file for a later retry or login.
The adapter does not fall back to `user/UID`, because that domain has a distinct service set and execution context.
Removal from an unavailable GUI domain fails conservatively and retains the file.
Retry removal after GUI login.
The caller should persist deletion before removal so an intervening launcher cannot revive an instance.

Launchd skips interval firings while the job runs or the machine sleeps.
A later interval retries reconciliation after the job finishes or the machine wakes.
This is login startup, not a system boot daemon.

## Durability and ownership

Generated files have an exact first-line `shelleyctl generated launcher v1` marker.
The adapter rejects symlinks, foreign markers, other owners, and group- or world-writable files.
It checks all destinations before replacing any launcher.
It publishes synced temporary files atomically and synchronizes the containing directory.
An absent destination uses a hard link to avoid overwriting a newly created foreign file.
A persistent advisory lock serializes modifications by concurrent CLI processes.
As with other user configuration files, the containing directory must be trusted against concurrent changes by unrelated programs.

Removal disables the scheduler before stopping an invocation and deleting its files.
Manager failures leave launcher files available for retry.
Linux removal also retries manager reload when the files are already absent.
`Installed` checks durable files, not session-manager health or enablement.
A command that is missing or fails is not treated as a missing service.
Launchd's specific missing-service diagnostic is the only query failure treated as service absence.

Tests use temporary directories and fake manager commands discovered through `PATH`.
The Linux syntax test also runs read-only `systemd-analyze --user verify` when available.
Tests never enable host services or edit root configuration.

## Sources

- [systemd timer semantics](https://www.freedesktop.org/software/systemd/man/latest/systemd.timer.html)
- [systemd command and environment syntax](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html#Command%20lines)
- [launchctl manual: domains and modern lifecycle commands](https://keith.github.io/xcode-man-pages/launchctl.1.html)
- [launchd.plist manual: interval behavior](https://keith.github.io/xcode-man-pages/launchd.plist.5.html)

The launchctl manual distinguishes GUI and user domains and warns that `print` output is not a stable structured API.
The adapter uses domain command success and the missing-service diagnostic, not parsed domain properties.

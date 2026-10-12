# Native runtime adapters

`New("linux")` uses `podman`.
`New("darwin")` uses Apple's `container` CLI.
Commands resolve through `PATH` and run without a shell.
`Ensure` creates a stopped container.
Call `Start` separately to run it.
Names can be instance names or complete `shelleyctl-` names.

## Resource safety

The adapter records the requested image, home, and port in `io.shelleyctl.image`, `io.shelleyctl.home`, and `io.shelleyctl.port` labels.
It also sets `io.shelleyctl.managed=true`.
`Ensure`, `Start`, and `Remove` check those labels against the actual runtime configuration before they reuse or modify a container.
Containers with missing labels require manual migration or removal.
The adapter does not adopt them automatically.

The home mount must be writable at `/home/shelley`.
The only published port must be `127.0.0.1:<port>:9000/tcp`.
The adapter rejects additional mounts and published ports.
`Inspect` can report foreign resources but does not authorize mutations.

A successful JSON list query establishes resource absence.
A daemon, permission, inspect, or JSON error never establishes absence.
Podman mutations use the inspected container ID to avoid name-replacement races.
Apple's CLI uses the name as the container ID, so concurrent external name replacement cannot be made atomic through this CLI.
Do not concurrently replace Apple containers outside shelleyctl.

## Podman signature policy

Some standalone Podman installations, including Homebrew installations, provide `policy.json` outside Podman's default search locations.
The adapter does not create an `insecureAcceptAnything` policy or disable signature verification.
Install the distribution's containers policy in its documented location, or explicitly select an existing administrator-approved policy:

```sh
export SHELLEYCTL_SIGNATURE_POLICY=/path/to/approved/policy.json
```

On Linux, `Build` passes that path through Podman's `--signature-policy` option.
The adapter leaves the default trust configuration unchanged when this variable is absent.
Apple builds ignore this Podman-specific variable.
Build failures include native stderr and a policy configuration hint.

## Protocol references and validation

Apple 0.4.1 emits a JSON array of `ContainerSnapshot` objects with `configuration` and string `status` fields.
The adapter also reads newer Apple's structured `status.state` field.
Swift Codable encodes a virtiofs mount type as `{"virtiofs":{}}`.

- [Apple container 0.4.1 flags](https://github.com/apple/container/blob/0.4.1/Sources/ContainerClient/Flags.swift)
- [Apple container snapshot](https://github.com/apple/container/blob/0.4.1/Sources/ContainerClient/Core/ContainerSnapshot.swift)
- [Apple filesystem JSON types](https://github.com/apple/container/blob/0.4.1/Sources/ContainerClient/Core/Filesystem.swift)
- [Apple published port JSON types](https://github.com/apple/container/blob/0.4.1/Sources/ContainerClient/Core/PublishPort.swift)
- [Newer Apple managed container JSON](https://github.com/apple/container/blob/main/Sources/ContainerResource/Container/ManagedContainer.swift)
- [Podman inspect documentation](https://docs.podman.io/en/latest/markdown/podman-container-inspect.1.html)

Tests use temporary fake executables through the production `PATH` boundary.
No exported test seam or runtime dependency is needed.
Live Podman operations were unavailable in the restricted development sandbox.
The configured Studio SSH host did not resolve, so Apple protocol research used published source instead of a live Studio session.

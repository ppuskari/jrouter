# GlobalTalk AURP Router 1.0

**GlobalTalk AURP Router** is a modern AppleTalk router with EtherTalk and AppleTalk Update-Based Routing Protocol (AURP) support. Version **1.0.0** is the first stable release from the RFC 1504 implementation and field-soak line maintained in this repository.

The executable remains named `jrouter` internally and in configuration compatibility surfaces. Release binaries use the product name in their filenames.

## 1.0 capabilities

- EtherTalk routing on modern systems.
- AURP peer routing for GlobalTalk and other compatible AURP deployments.
- RFC 1504 core routing plus the Chapter 4 behavior tracked in [`docs/RFC1504-CONFORMANCE.md`](docs/RFC1504-CONFORMANCE.md).
- Hard-seed, soft-seed, and non-seed EtherTalk operation.
- Route and zone learning and propagation.
- Split-horizon and route-lifecycle hardening.
- Bounded AURP routing/zone datagrams and bounded ZIP request sets.
- Static network remapping.
- HTTP operator status, detailed peering status, health/readiness JSON, and Prometheus metrics.
- Per-route DDP byte counters and top-talker ordering on the routing status view.

Dynamic network remapping is not implemented; RFC 1504 permits static or dynamic remapping. The historical Apple Internet Router SNMP MIB is not implemented; this project exposes modern status and metrics interfaces instead.

## Release identity

The stable release version is stored in `meta/VERSION`. Official binaries embed the exact source Git SHA and report it with:

```text
jrouter -version
```

For 1.0.0 the expected form is:

```text
jrouter v1.0.0 build <40-character-git-sha>
```

SHA-256 checksum files are published with each binary.

## Supported 1.0 release binaries

The initial 1.0 release publishes:

- `globaltalk-aurp-router-v1.0.0-linux-amd64`
- `globaltalk-aurp-router-v1.0.0-windows-amd64.exe`

Both are produced from the same source commit by the `GlobalTalk AURP Router 1.0.0` GitHub Actions workflow.

### Windows runtime note

Windows packet capture uses the gopacket Windows pcap path. Install **Npcap** on the target system before using EtherTalk capture/injection. The Windows binary itself is built without a C compiler dependency.

## Configuration

Create a `jrouter.yaml` file using [`jrouter.yaml`](jrouter.yaml) as the configuration reference. By default `jrouter` looks for its configuration in the current directory; specify another path with:

```text
jrouter -config /path/to/jrouter.yaml
```

AURP normally uses UDP port 387. EtherTalk requires access to raw Ethernet frames, so the process must have the necessary operating-system privileges.

### Linux capabilities

A typical Linux installation can grant the binary the required privileges without running it permanently as root:

```shell
sudo setcap 'CAP_NET_BIND_SERVICE=ep CAP_NET_RAW=ep' ./globaltalk-aurp-router-v1.0.0-linux-amd64
```

## Operator status

Set `monitoring_addr` in the configuration and browse to the configured HTTP listener.

- `/status` — operator-oriented router and routing-table status.
- `/peering` — detailed AURP peer state and diagnostics.
- `/metrics` — Prometheus metrics.

The routing table includes aggregate DDP bytes in/out so active routes and top talkers can be identified quickly.

## Building

Go **1.26.4** is declared by `go.mod` for this release line.

On Debian/Ubuntu Linux:

```shell
sudo apt install git build-essential libpcap-dev
go test ./...
go build .
```

The final release workflow performs gofmt validation, `go vet`, unit tests, race tests on the critical router/AURP/ZIP/status packages, repeated AURP stress tests, full package builds, and Linux/Windows amd64 release builds.

## Preparing and publishing 1.0.0 from Windows PowerShell

From a clean local clone of `ppuskari/jrouter`:

```powershell
.\scripts\Prepare-Release1.ps1
.\scripts\Download-Release1.ps1
```

Test both downloaded binaries. When the final validation is satisfactory:

```powershell
.\scripts\Publish-Release1.ps1
```

The publish script refuses to proceed if the working tree is dirty, the local release branch differs from GitHub, the verified CI artifacts do not come from that exact commit, the version is not `1.0.0`, or an incompatible `v1.0.0` tag/release already exists.

## Protocol references

- Apple Computer, *AppleTalk Update-Based Routing Protocol: Enhanced AppleTalk Routing* (RFC 1504 / Apple AURP documentation).
- Sidhu, Andrews & Oppenheimer, *Inside AppleTalk, Second Edition*.
- Apple Internet Router 3.0 behavior and interoperability.

See [`docs/RFC1504-CONFORMANCE.md`](docs/RFC1504-CONFORMANCE.md) for the repository's conformance matrix and implementation notes.

## Project lineage and license

This repository is derived from Josh Deprez's `jrouter` implementation and retains the original Apache License 2.0 notices and attribution in the source tree. The GlobalTalk AURP Router 1.0 release line adds the AURP/RFC 1504 completion, resiliency, failover, observability, and operator-facing work developed and field-tested in this fork.

See [`LICENSE`](LICENSE) for license terms.

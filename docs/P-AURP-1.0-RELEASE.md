# P-AURP 1.0 release notes

## Release identity

- Product: **P-AURP 1.0**
- First public P-AURP 1.0 patch release: **v1.0.1**
- Release branch: `release/p-aurp-v1.0-20260906`
- Proven protocol/data-plane base: `aa02f412d33cae0de2c7c608f942413359a24fe1`
- Platforms: Linux amd64 and Windows amd64

The historical Go module/repository name remains `drjosh.dev/jrouter` / `jrouter`
for source compatibility. Public executable identity is `P-AURP`.

## What P-AURP 1.0 contains

P-AURP 1.0 is an EtherTalk/AURP router focused on practical GlobalTalk routing,
RFC 1504 behavior and operator visibility. The 1.0 release line includes:

- AURP routing and peer lifecycle handling;
- RTMP and ZIP route/zone exchange;
- NBP forwarding across the routed AppleTalk internetwork;
- hard-seed, soft-seed and true non-seed EtherTalk operation;
- static network remapping and RFC 1504 policy features implemented by the
  release line;
- split-horizon and route ownership/provenance protections;
- bounded/chunked routing and zone information exchange;
- per-route DDP byte telemetry for top-talker identification;
- `/status` and `/peering` operator views plus health/readiness and Prometheus
  endpoints;
- Windows friendly-interface-name to Npcap-device mapping;
- correct EtherTalk handling of DDP destination node 0 ("any router") as a
  broadcast destination;
- correct AEP echo replies after packet mutation by clearing the stale received
  DDP checksum before transmit.

## Windows-specific fixes proven in v1.0.1

### Friendly Npcap interface mapping

Windows configuration can use the adapter's friendly name, such as `Ethernet`
or `Ethernet InUse`. P-AURP maps the Windows interface index to the matching
Npcap `\\Device\\NPF_{GUID}` device internally.

### EtherTalk node-zero forwarding

DDP destination node `0` is now treated like `0xFF` for EtherTalk transmission,
so "any router" packets are emitted as Ethernet AppleTalk broadcasts rather
than being incorrectly sent through AARP unicast resolution.

### AEP reply checksum correctness

The AEP responder reverses the received DDP addresses/sockets and changes the
payload from EchoRequest to EchoReply. That mutation invalidates the received
DDP checksum. P-AURP now clears that stale checksum, using the valid AppleTalk
zero-checksum form for the generated response.

## Field validation

The clean v1.0.1 RC3 Windows data plane was run continuously for more than
**56 hours** before promotion.

The final soak included:

- more than **2.3 GB** of local EtherTalk DDP bytes on one direction of the
  local route, with additional traffic in the reverse direction;
- multi-gigabyte AURP route traffic;
- sustained AFP transfers involving physical classic Macintosh hardware and
  Netatalk virtual machines;
- live ZIP `getzones` discovery;
- NBP traversal and GlobalTalk service inventory;
- local AEP echo to P-AURP itself;
- remote AEP echo across GlobalTalk/AURP;
- dynamic non-seed EtherTalk cable/zone discovery;
- continuous AURP route churn, additions, withdrawals and distance updates;
- stable goroutine count with no obvious long-run goroutine leak.

The validated RC3 identity was:

```text
jrouter v1.0.1-rc3 build aa02f412d33cae0de2c7c608f942413359a24fe1
```

The P-AURP release changes after that commit are identity, documentation,
examples and release packaging only; the proven routing/data-plane behavior is
not altered.

## Windows topology note

A same-host Windows/Npcap/VirtualBox configuration in which P-AURP and a bridged
guest share the same physical NIC can encounter an NDIS/filter-order hairpin
limitation. A proven arrangement is to use one physical Windows NIC for
Npcap/P-AURP and a second physical NIC for VirtualBox bridged guests, with both
connected to the same external Ethernet switch.

That dual-NIC topology successfully carried RTMP, ZIP, NBP, AEP, AURP and AFP
traffic end-to-end.

## Configuration

Use the platform-specific annotated examples:

- `examples/jrouter-linux.yaml`
- `examples/jrouter-windows.yaml`

On Windows, keep the runtime file named `jrouter-windows.yaml` and pass it
explicitly with `-config`. This prevents the Windows deployment from
accidentally starting with the generic repository `jrouter.yaml`.

## Build and deployment

See `docs/P-AURP-1.0-BUILD.md` for reproducible Linux and Windows builds,
release identity checks, runtime commands, detached Windows operation and
post-start validation.

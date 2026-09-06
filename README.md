# P-AURP 1.0

**P-AURP 1.0** is a cross-platform EtherTalk/AURP router for modern Linux and
Windows systems, with a strong focus on practical GlobalTalk operation,
RFC 1504 behavior, observability and compatibility with classic AppleTalk
networks.

The first public P-AURP 1.0 patch release is **v1.0.1**.

The historical repository/module name remains `jrouter` / `drjosh.dev/jrouter`
for source compatibility, but the public release identity is **P-AURP**.

## Release status

P-AURP 1.0 was promoted from clean v1.0.1 RC3 after more than 56 hours of
continuous Windows routing with multi-gigabyte DDP/AURP traffic, live AFP
transfers, ZIP zone discovery, NBP traversal, local and remote AEP, dynamic
non-seed EtherTalk operation and continuous AURP route churn.

See:

- [P-AURP 1.0 release notes](docs/P-AURP-1.0-RELEASE.md)
- [Linux and Windows build/deployment guide](docs/P-AURP-1.0-BUILD.md)
- [RFC 1504 conformance matrix](docs/RFC1504-CONFORMANCE.md)

## Major capabilities

- EtherTalk Phase 2 routing
- AURP over UDP, including RFC 1504 routing behavior
- RTMP and ZIP route/zone exchange
- NBP forwarding across routed AppleTalk networks
- AEP echo handling
- hard-seed, soft-seed and true non-seed EtherTalk operation
- static network remapping
- peer-scoped import/export policy controls
- route ownership/provenance and split-horizon protections
- bounded/chunked routing and zone-information exchange
- per-route DDP byte telemetry for top-talker visibility
- operator `/status` and `/peering` pages
- `/healthz`, `/readyz`, `/api/v1/aurp` and Prometheus `/metrics`
- Windows friendly adapter-name to Npcap capture-device mapping
- Linux and Windows amd64 release builds from one shared Go protocol core

## Quick start: Linux

Install prerequisites on Debian/Ubuntu/Raspberry Pi OS:

```bash
sudo apt update
sudo apt install -y git build-essential libpcap-dev
```

Build:

```bash
git clone https://github.com/ppuskari/jrouter.git
cd jrouter
git checkout release/p-aurp-v1.0-20260906
mkdir -p dist
BUILD_SHA="$(git rev-parse HEAD)"
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build \
  -ldflags "-X drjosh.dev/jrouter/meta.Build=${BUILD_SHA}" \
  -o dist/p-aurp-v1.0.1-linux-amd64 .
```

Create the runtime config:

```bash
cp examples/jrouter-linux.yaml ./jrouter-linux.yaml
```

Grant the Linux capabilities required for raw EtherTalk and UDP 387:

```bash
sudo setcap 'CAP_NET_BIND_SERVICE=ep CAP_NET_RAW=ep' \
  ./dist/p-aurp-v1.0.1-linux-amd64
```

Run:

```bash
./dist/p-aurp-v1.0.1-linux-amd64 \
  -config ./jrouter-linux.yaml
```

## Quick start: Windows

Install Npcap, then build from PowerShell:

```powershell
Set-Location 'C:\AppleIIgsDev\jrouter'
git checkout release/p-aurp-v1.0-20260906
if ($LASTEXITCODE -ne 0) { throw 'git checkout failed.' }

New-Item -ItemType Directory -Path '.\dist' -Force | Out-Null
$BuildSHA = (git rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0) { throw 'git rev-parse failed.' }

$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'

go build -ldflags "-X drjosh.dev/jrouter/meta.Build=$BuildSHA" -o '.\dist\p-aurp-v1.0.1-windows-amd64.exe' .
if ($LASTEXITCODE -ne 0) { throw 'Windows build failed.' }
```

Create the Windows runtime config:

```powershell
Copy-Item '.\examples\jrouter-windows.yaml' '.\jrouter-windows.yaml'
```

**Windows deployments should use `jrouter-windows.yaml` explicitly.**

Find the friendly adapter name with:

```powershell
Get-NetAdapter | Select-Object Name,Status,MacAddress,LinkSpeed
```

Put that friendly name in the YAML `device:` field, then start P-AURP:

```powershell
& '.\dist\p-aurp-v1.0.1-windows-amd64.exe' -config '.\jrouter-windows.yaml'
```

P-AURP maps the friendly Windows adapter name to the correct Npcap
`\\Device\\NPF_{GUID}` capture device internally.

For detached Windows operation with redirected logs, see the full build and
deployment guide.

## Annotated configuration examples

- [Linux annotated example](examples/jrouter-linux.yaml)
- [Windows annotated example](examples/jrouter-windows.yaml)

The examples default to `seed_mode: none`, allowing P-AURP to discover the
existing cable range and default zone dynamically. They also document the
required fields for hard- and soft-seed operation.

## Operator pages

With:

```yaml
monitoring_addr: ":9459"
```

P-AURP exposes:

- `/status` - EtherTalk, AARP, routes, zones and route traffic
- `/peering` - detailed AURP peer/session state
- `/healthz` - health endpoint
- `/readyz` - readiness endpoint
- `/api/v1/aurp` - AURP summary data
- `/metrics` - Prometheus metrics

## Windows + VirtualBox note

A same-host Windows/Npcap/VirtualBox deployment sharing one physical NIC can
hit an NDIS/filter-order hairpin limitation. A field-proven arrangement is:

```text
Windows NIC 1 -> Npcap / P-AURP
Windows NIC 2 -> VirtualBox bridged guests
Both NICs     -> same physical Ethernet switch
```

That topology has been proven with RTMP, ZIP, NBP, AEP, AURP and AFP traffic.

## Build identity

Release builds embed the exact Git commit:

```text
P-AURP v1.0.1 build <full-git-sha>
```

Check any binary with:

```text
p-aurp... -version
```

## Project history and acknowledgements

P-AURP is based on the original `jrouter` project by Josh Deprez and retains
its Apache-2.0 licensing and source history. The project also builds on the
AppleTalk/AURP specifications and the open-source libraries already credited
in the repository history, including `sfiera/multitalk`, `gopacket`/`libpcap`
and the Prometheus Go client.

Important historical references include:

- *Inside AppleTalk*, 2nd edition
- Apple Computer's *AppleTalk Update-Based Routing Protocol: Enhanced
  AppleTalk Routing*
- Apple Internet Router 3.0

See the repository `LICENSE` file for licensing terms.

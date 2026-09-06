# P-AURP 1.0 build and deployment guide

P-AURP 1.0 is the public product name for this release line. The software
SemVer for the first public P-AURP 1.0 patch release is **1.0.1**. The Go module
and repository retain the historical `drjosh.dev/jrouter` / `jrouter` names for
source compatibility.

The P-AURP 1.0 release branch is:

```text
release/p-aurp-v1.0-20260906
```

The field-proven protocol/data-plane base is clean RC3 commit:

```text
aa02f412d33cae0de2c7c608f942413359a24fe1
```

Only release identity, documentation, examples and packaging are changed after
that green RC3 base.

## Common requirements

- Go 1.26.4 (the version declared by `go.mod`).
- Git.
- A working Ethernet interface connected to the AppleTalk cable/LAN.
- UDP 387 reachable for AURP peers unless you intentionally configure a
  different `listen_port`.
- TCP 9459 reachable from your management network if you use the example
  monitoring address.

Before building, confirm the release branch:

```text
git status --short --branch
git rev-parse HEAD
```

## Linux build

Debian/Ubuntu/Raspberry Pi OS prerequisites:

```bash
sudo apt update
sudo apt install -y git build-essential libpcap-dev
```

Clone and select the P-AURP 1.0 release branch:

```bash
git clone https://github.com/ppuskari/jrouter.git
cd jrouter
git checkout release/p-aurp-v1.0-20260906
```

Run the release gates:

```bash
gofmt -w $(git ls-files '*.go')
git diff --exit-code
go vet ./...
go test ./...
go test -race ./router ./aurp ./atalk/zip ./status
```

Build an amd64 release binary with the exact Git commit embedded:

```bash
mkdir -p dist
BUILD_SHA="$(git rev-parse HEAD)"
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build \
  -ldflags "-X drjosh.dev/jrouter/meta.Build=${BUILD_SHA}" \
  -o dist/p-aurp-v1.0.1-linux-amd64 .
```

Verify identity:

```bash
./dist/p-aurp-v1.0.1-linux-amd64 -version
```

Expected form:

```text
P-AURP v1.0.1 build <full-git-sha>
```

Create a Linux config from the annotated example:

```bash
cp examples/jrouter-linux.yaml ./jrouter-linux.yaml
nano ./jrouter-linux.yaml
```

For direct non-root execution on Linux, grant the capabilities needed for raw
EtherTalk access and the traditional AURP privileged UDP port:

```bash
sudo setcap 'CAP_NET_BIND_SERVICE=ep CAP_NET_RAW=ep' \
  ./dist/p-aurp-v1.0.1-linux-amd64
```

Start P-AURP explicitly with the Linux config:

```bash
./dist/p-aurp-v1.0.1-linux-amd64 \
  -config ./jrouter-linux.yaml
```

For a system service, use the same explicit `-config` argument from systemd and
send SIGTERM for a graceful shutdown.

## Windows build

### Runtime prerequisite

Install Npcap on the Windows host. P-AURP accepts the friendly Windows adapter
name from `Get-NetAdapter` and maps it internally to the correct Npcap capture
path. An elevated PowerShell session is recommended for build/runtime testing.

Select the release branch:

```powershell
Set-Location 'C:\AppleIIgsDev\jrouter'
git fetch origin
git checkout release/p-aurp-v1.0-20260906
if ($LASTEXITCODE -ne 0) { throw 'git checkout failed.' }
```

Run the principal release gates:

```powershell
go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'go vet failed.' }

go test ./...
if ($LASTEXITCODE -ne 0) { throw 'go test failed.' }

go test -v . -run TestPcapDeviceNameWindows
if ($LASTEXITCODE -ne 0) { throw 'Windows Npcap mapping test failed.' }

go test -v ./router -run TestEtherTalkBroadcastNode
if ($LASTEXITCODE -ne 0) { throw 'EtherTalk node-zero test failed.' }

go test -v ./router -run TestPrepareAEPReplyClearsStaleChecksum
if ($LASTEXITCODE -ne 0) { throw 'AEP checksum test failed.' }
```

Build the Windows amd64 executable:

```powershell
New-Item -ItemType Directory -Path '.\dist' -Force | Out-Null
$BuildSHA = (git rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0) { throw 'git rev-parse failed.' }

$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'

go build -ldflags "-X drjosh.dev/jrouter/meta.Build=$BuildSHA" -o '.\dist\p-aurp-v1.0.1-windows-amd64.exe' .
if ($LASTEXITCODE -ne 0) { throw 'Windows amd64 build failed.' }
```

Verify the executable identity:

```powershell
& '.\dist\p-aurp-v1.0.1-windows-amd64.exe' -version
if ($LASTEXITCODE -ne 0) { throw 'Version check failed.' }
```

### Windows configuration rule

**Windows must use its Windows-specific configuration file.** Do not rely on
the generic `jrouter.yaml` when you have a working Windows NIC/Npcap setup.
Create the runtime file from the annotated example:

```powershell
Copy-Item '.\examples\jrouter-windows.yaml' '.\jrouter-windows.yaml'
notepad '.\jrouter-windows.yaml'
```

Find the friendly adapter name:

```powershell
Get-NetAdapter | Select-Object Name,Status,MacAddress,LinkSpeed
```

Put that exact friendly name in:

```yaml
ethertalk:
  - device: Ethernet
```

Then start P-AURP with the config path explicitly supplied:

```powershell
& '.\dist\p-aurp-v1.0.1-windows-amd64.exe' -config '.\jrouter-windows.yaml'
```

### Detached Windows operation

For a long soak or unattended router, redirect stdout/stderr so console
selection or an interactive PowerShell window cannot stall the process:

```powershell
$Exe = 'C:\AppleIIgsDev\jrouter\dist\p-aurp-v1.0.1-windows-amd64.exe'
$Repo = 'C:\AppleIIgsDev\jrouter'
$Config = 'C:\AppleIIgsDev\jrouter\jrouter-windows.yaml'
$LogDir = 'C:\AppleIIgsDev\jrouter\logs'

New-Item -ItemType Directory -Path $LogDir -Force | Out-Null
$Stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$StdOut = Join-Path $LogDir "p-aurp-$Stamp.out.log"
$StdErr = Join-Path $LogDir "p-aurp-$Stamp.err.log"

$Process = Start-Process -FilePath $Exe -ArgumentList '-config', $Config -WorkingDirectory $Repo -RedirectStandardOutput $StdOut -RedirectStandardError $StdErr -WindowStyle Hidden -PassThru

Start-Sleep -Seconds 3
if ($Process.HasExited) {
Write-Host '----- STDERR -----'
if (Test-Path $StdErr) { Get-Content $StdErr }
Write-Host '----- STDOUT -----'
if (Test-Path $StdOut) { Get-Content $StdOut }
throw "P-AURP exited with code $($Process.ExitCode)."
}

Write-Host "P-AURP PID: $($Process.Id)"
Write-Host "stdout: $StdOut"
Write-Host "stderr: $StdErr"
```

Stop a specific detached process by PID:

```powershell
Stop-Process -Id 12345
```

Use `-Force` only if a normal stop does not complete.

## Windows + VirtualBox topology note

A same-host Windows/Npcap/VirtualBox deployment that shares one physical NIC can
hit an NDIS/filter-order hairpin limitation: the host router and a bridged guest
may not see each other's injected/captured EtherTalk traffic reliably.

A field-proven topology is:

```text
Windows host NIC 1 -> Npcap / P-AURP
Windows host NIC 2 -> VirtualBox bridged guests
Both NICs          -> same physical Ethernet switch
```

This forces guest traffic through the real switch before it reaches P-AURP and
was proven with full ZIP zone discovery, NBP inventory, AEP and AFP traffic.

## Post-start validation

Open the status page configured by `monitoring_addr`, normally:

```text
http://<router-ip>:9459/status
```

Verify:

- the expected EtherTalk device is present;
- AARP shows `Status: Assigned address ...` and `Address phase: operational`;
- the cable range/zone are correct;
- the local route is distance 0;
- AURP peers are connected or retrying normally;
- `/peering` shows live peer state;
- route DDP byte counters increase during real traffic.

From a Netatalk/AppleTalk Linux host, useful validation includes:

```bash
getzones
aecho -c 3 <current-P-AURP-AppleTalk-address>
nbplkup '=:=@*'
```

Always use the **current** AppleTalk address shown by the P-AURP status page;
non-seed startup may choose a different node address on a later restart.

## P-AURP 1.0 field proof

The clean Windows RC3 data plane was continuously soaked for more than
56 hours on Windows amd64 before promotion. The observed local EtherTalk route
carried approximately 2.4 GB of DDP traffic while AURP route traffic included
multi-gigabyte flows. ZIP/getzones, NBP traversal, local and remote AEP, AFP
transfers, dynamic non-seed EtherTalk operation and ongoing AURP route churn all
remained operational with a stable goroutine count.

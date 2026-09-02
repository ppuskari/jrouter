# Set28 Release-Candidate Checklist

Set28 is the release-candidate preparation line. It starts from the exact green
Set27 tranche-2 checkpoint:

`270a4959117419541f758839d5b944ae5c300b3c`

No new AURP architecture is planned for this line. Changes are limited to
defect fixes, conformance proof, performance measurement, operational
presentation, and release packaging.

## 1. Protocol conformance closure

The following must be covered by deterministic tests before RC:

- malformed and truncated domain, Open, RI, RD, Tickle, and Zone packets;
- duplicate, stale, future, reordered, and wrong-connection routing packets;
- RI-Rsp and RI-Upd loss/retry behavior;
- extended ZI fragmentation, loss, duplicate fragments, and completion;
- Loop Probe recognition data, minimum retry count and spacing, wrong-port
  return, local-internet return, and confirmed-loop shutdown;
- remap plus NBP rewrite with valid, zero, and invalid DDP checksums;
- device hiding combined with remapping;
- cluster zone union and clustered NBP FwdReq expansion;
- primary -> backup -> primary restoration;
- DNS candidate endpoint change while the logical tunnel keeps route ownership.

## 2. Data-plane performance closure

Set27 added per-peer DDP packet/byte counters and receive-queue high-water
telemetry. Set28 should use those counters to separate router service rate from
AFP/ATP/WAN latency.

For each test transfer record:

- peer identity;
- elapsed transfer time;
- DDP bytes in/out delta;
- DDP packets in/out delta;
- receive-queue current and high-water values;
- CPU utilization and goroutine count;
- whether remap/device-hiding/weight/HCR policies are enabled;
- application-observed throughput.

Required result for RC: no sustained receive-queue growth or unexplained router
service bottleneck under the normal zero-policy configuration. Any throughput
limit must be attributable either to the AppleTalk application/protocol path,
WAN latency, or a measured jrouter forwarding cost.

## 3. Field-soak closure

Use the Set27/Set28 health and AURP APIs during the soak.

A candidate remains acceptable when:

- `ready` stays true during normal operation;
- `loop_disabled` remains zero unless a deliberately injected loop is tested;
- receive queues return to zero after bursts and do not grow without bound;
- peer reconnect/DNS failures recover without orphaned route ownership;
- routing-table alternatives remain deterministic;
- no panic, deadlock, data race, or goroutine leak is observed;
- Router Down/reconnect behavior remains clean during shutdown and restart.

Keep the locked Set27 checkpoint available as the A/B control.

## 4. CI release gate

Every RC candidate must pass:

1. global gofmt;
2. `go vet ./...`;
3. `go test ./...`;
4. race tests for router/AURP/ZIP/status;
5. Phase2/AURP stress at 100 repetitions;
6. full `go build ./...`;
7. exact-commit Linux amd64 build with embedded SHA;
8. artifact SHA-256 publication.

No failed or cancelled intermediate run counts as release evidence; the exact
candidate SHA must have one complete green run.

## 5. Operations and presentation

Before RC:

- status table headings must be self-describing;
- `/healthz`, `/readyz`, and `/api/v1/aurp` must document their fields;
- every optional Chapter 4 policy must have a configuration example;
- version output must include both semantic version and exact build SHA;
- README caveats must match current behavior;
- the RFC 1504 conformance matrix must identify implemented, optional, and
  intentionally out-of-scope features.

Build/download procedure: [Set28 Build and Download Helpers](SET28-BUILD-AND-DOWNLOAD.md).

## 6. Release packaging

The first candidate should be named `1.0.0-rc1` only after Set28 closes the
items above. Until then the development version remains `0.0.28`.

For the RC checkpoint preserve:

- immutable lock branch;
- annotated Git tag;
- exact source SHA;
- CI run ID;
- Linux artifact ID;
- binary SHA-256;
- field-soak start/end observations;
- known limitations.

After RC1, accept only defect fixes, test additions, documentation corrections,
and release/packaging changes unless a protocol interoperability blocker is
proven.

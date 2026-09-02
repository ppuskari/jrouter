# RFC 1504 Conformance Matrix

This document records the intended AURP conformance boundary for jrouter 1.0.
It distinguishes required protocol behavior from optional RFC 1504 Chapter 4
enhancements and from historical Apple Internet Router management facilities.

The release goal is not merely to recognize packet types. The implementation
must preserve routing state across loss, duplication, restart, DNS endpoint
changes, and long-running field operation.

## Core AURP protocol

| Area | Status | Implementation / evidence |
| --- | --- | --- |
| Domain header, IP domain identifiers | Implemented | AURP parser/transport plus stable DI display and immutable parsed DI ownership tests. |
| Open-Req / Open-Rsp | Implemented | Version 1, SUI flags, unsupported options ignored by the peer FSM, malformed option tuples rejected, and successful Open-Rsp packets advertise active remapping/HCR environment flags plus the configured nominal update rate. |
| RI-Req / RI-Rsp | Implemented | Sequenced initial routing exchange, bounded multi-packet RI-Rsp, duplicate re-ACK, restart recovery, and RI-Req deferral while another sequenced packet remains unacknowledged. |
| RI-Ack | Implemented | Connection/sequence validation, duplicate recovery, Router Down acknowledgement handling. |
| RI-Upd | Implemented | Incremental NA/ND/NRC/NDC events, SUI filtering, ACK gating, event coalescing. |
| Router Down | Implemented | Receiver- and sender-originated RD, normal close and routing-loop error, retry/ACK shutdown semantics. |
| Tickle / Tickle-Ack | Implemented | Receiver liveness, configurable timers/retry limits, late/duplicate diagnostics, and the >2-minute LHFT tickle-before-data rule. |
| Zone Information | Implemented | ZI-Req/ZI-Rsp, extended fragments, retry-until-complete, precise SZI behavior. |
| GDZL / GZN | Implemented | Requests and responses supported; unsupported cases return protocol responses rather than silently hanging. |
| Large routing updates | Implemented | AURP routing tuples are split below the configured datagram budget and remain ACK-gated. |
| Large ZIP queries | Implemented | Query network lists are sorted, deduplicated, and chunked to at most 255 networks. |
| Stable tunnel identity | Implemented | Configured peer identity is independent of the active DNS-resolved endpoint. |
| DNS candidate failover | Implemented | Multiple IPv4 candidates, backoff, candidate refresh, stable route ownership. |

## Chapter 4 enhancements

| Enhancement | Status | Notes |
| --- | --- | --- |
| Network hiding (export) | Implemented | Hidden local ranges are omitted from routing/zone export and remote traffic to them is dropped. |
| Network hiding (import) | Implemented | Peer-scoped remote ranges can be suppressed on import. |
| Device hiding | Implemented | Peer/direction-scoped NBP lookup-reply filtering with checksum-safe rewrite. |
| Network remapping | Implemented, static | Static peer-scoped equal-sized range mappings rewrite routing, DDP, and NBP addresses. Dynamic allocation is not implemented. |
| Remapping checksum rules | Implemented | Nonzero DDP checksum is verified before rewrite; rewritten packets use checksum zero as permitted by AppleTalk. |
| Clustering | Implemented | Remapped networks can be advertised as an extended cluster with zone union and NBP FwdReq expansion. |
| Loop-indicative route detection | Implemented | Direct local range size plus exact zone signature comparison. |
| Loop Probe | Implemented | RTMP function 4, recognition token, at least four attempts, at least two seconds apart, local-return confirmation. |
| Routing-loop shutdown | Implemented | Confirmed local Loop Probe return disables the suspect tunnel and uses routing-loop Router Down. |
| Hop-count reduction | Implemented | Guarded by Loop Probe support; prevents packets exceeding the 15-hop limit and presents AURP paths locally as one hop when enabled. |
| Hop-count weighting | Implemented | Route distance and forwarded DDP hop count can be increased by configured weight. |
| Alternative path forwarding | Implemented | A packet is not reflected to the AURP tunnel it arrived on when another valid route exists. |
| Backup paths | Implemented | Configured backup penalty retains a candidate without preferring it; tests cover primary -> backup -> primary restoration. |
| Network management visibility | Implemented with native interfaces | Status UI, /healthz, /readyz, /api/v1/aurp, and Prometheus metrics expose protocol and policy state. |

## Intentionally outside the 1.0 wire-protocol claim

- Dynamic remapping. RFC 1504 permits static configuration as an alternative,
  and jrouter currently implements static remapping.
- The historical Apple Internet Router SNMP MIB. This is a management interface,
  not a requirement for AURP packet interoperability. jrouter uses HTTP/JSON
  and Prometheus for management visibility.
- Recovery from a fixed-capacity routing-table overflow. jrouter's routing
  indexes are dynamically managed rather than using the fixed table model
  assumed by historical router implementations.
- Translation of arbitrary application protocols that embed AppleTalk network
  numbers in opaque payloads. RFC 1504 itself identifies this as a remapping
  limitation.

## Release gates

Set28 tracks these gates operationally in [the RC checklist](SET28-RC-CHECKLIST.md).
A 1.0 candidate should not be tagged until all of the following remain green:

1. Global gofmt and go vet.
2. Full unit suite.
3. Race tests for router, AURP, ZIP, and status paths.
4. Phase 2 / AURP stress suite at 100 repetitions.
5. Exact-commit Linux amd64 build with embedded provenance.
6. Malformed and truncated AURP/Open parser tests.
7. Primary/backup/primary restoration tests.
8. Loop Probe timing, recognition-data, and local-return tests.
9. Remap + NBP + checksum combination tests.
10. Long-running field soak with zero unexpected loop disables and bounded
    receive queues.
11. Data-plane throughput measurements using the AURP DDP byte/packet counters
    so protocol/application latency can be separated from router service rate.

The conformance matrix is expected to become stricter as release testing finds
edge cases. A feature marked implemented is still subject to these release
gates.

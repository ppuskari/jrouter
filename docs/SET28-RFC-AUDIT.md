# Set28 RFC 1504 Audit - September 2026

This audit rechecked the current Set28 implementation against RFC 1504 before
the first 1.0 release candidate.

## Chapter 3 closure found by the audit

Four small gaps were identified and closed in Set28:

- Successful Open-Rsp packets advertise the environment flags for
  network-number remapping and hop-count reduction when active for the peer.
- An RI-Req received while an RI-Rsp, RI-Upd, or Router Down remains
  outstanding no longer overwrites the active sequenced transaction. AURP-Tr
  retransmission supplies the request again after the outstanding packet is
  acknowledged.
- The RI-Upd interval is configurable as `aurp.update_interval`. It defaults
  to 10 seconds, cannot be below 10 seconds, and uses whole 10-second units so
  the Open-Rsp nominal-rate field remains exact.
- When last-heard-from timeout exceeds two minutes, a peer with two minutes of
  receiver-side inactivity sends a Tickle before the next encapsulated
  AppleTalk data packet. The normal 90-second default path is unchanged.

## Re-verified existing behavior

The audit rechecked connection IDs; sequence zero transactions and wrap;
n-1 re-ACK, n processing, n+1 reset and stale discard; SUI subscriptions;
NA/ND/NRC/NDC/Null/ZC handling; ACK-gated RI-Rsp and RI-Upd retries; reverse
null-RI-Upd liveness probing; Router Down; ZI/SZI and extended-zone recovery;
mandatory unsupported GDZL/GZN responses; split horizon; hiding; static
remapping; clustering; Loop Probe; HCR; weighting; alternative paths; and
backup paths.

## Remaining optional scope

- Dynamic remapping is not implemented; static remapping is supported.
- GDZL and GZN return the RFC-defined unsupported response.
- The historical SNMP MIB is replaced by status/JSON and Prometheus.
- Peer-specific export network hiding is implemented as
  `hidden_export_networks`; existing `hidden_networks` remains the
  all-peers policy and import hiding remains peer-scoped.
- Point-to-point foreign-link tunneling is outside jrouter's current
  IP/GlobalTalk transport target.

These optional items should not destabilize the proven default AURP/IP path.

## Additional Chapter 2/4 closure

The follow-up audit tightened two edge conditions:

- `aurp.retry_interval` cannot be configured below two seconds, preventing
  Open-Req retransmission faster than the RFC recommendation.
- A tunneled DDP packet already at hop count 15 is not forwarded onward to
  another router. It may still be delivered onto its directly connected
  destination network.

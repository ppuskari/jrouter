# jrouter v1.0.0-rc2 Route-Traffic Telemetry

RC2 preserves the RC1 AURP protocol implementation and adds routing-table
traffic accounting for operational triage.

The routing-table status now displays:

`DDP bytes in/out`

for each route entry.

Semantics:

- **in** is the cumulative DDP byte count sourced by the route's network/range;
- **out** is the cumulative DDP byte count successfully routed toward that
  route;
- DDP byte counts include the 13-byte extended DDP header and payload;
- AURP ingress is attributed to the route belonging to the actual ingress
  logical peer when multiple routes exist for the source network;
- counters use atomics and are shared across route-table copies;
- ordinary route refreshes preserve counters rather than resetting the row;
- removing a route and later relearning it creates a new route lifecycle and
  therefore starts new counters.

This is observability-only. Route choice, AURP wire behavior, routing timers,
and forwarding policy are unchanged from RC1.

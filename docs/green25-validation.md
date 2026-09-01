# GREEN25 post-24 cleanup

## Changelog

- Clarify AURP RI-Rsp/RI-Upd state and tuple diagnostics. Valid late RI-Rsp
  refreshes are processed without a misleading warning; duplicate and stale
  sequenced packets remain idempotent and are re-acknowledged or dropped as
  required by RFC 1504.
- Preserve route-range validation and make ignored malformed, distance-15, and
  too-high route tuples explicit in logs.
- Render four-byte AURP domain identifiers as IPv4 plus deterministic hex in
  packet/log/status presentation, without changing tunnel identity keys.
- Normalize embedded status fragments so tabs and escaped tab entities do not
  leak into the rendered UI.
- Bump the single candidate to jrouter v0.0.25 (GREEN25).

## Validation checklist

- [x] `go test ./...` passes on the Windows build host.
- [x] Focused AURP tests cover late RI-Rsp processing, duplicate idempotence,
      tuple safeguards, distance-15 handling, and deterministic DI display.
- [x] Status smoke tests cover clean HTML fragment presentation.
- [x] Candidate artifact reports `jrouter v0.0.25`:
      `build/green25/jrouter_0.0.25_windows_amd64.exe`.
- [x] Candidate checksum:
      `54a16af6e4f908f560be65f660082d28716dc42e5c2cae22da17bdb68b22e527`.
- [x] `go test -count=1 ./...` passes under Linux/amd64 with Go 1.26.4,
      gcc, and libpcap.
- [x] `go test -race -count=1 ./aurp ./router ./status` passes under
      Linux/amd64.
- [x] Linux/amd64 production artifact:
      `build/green25/jrouter_0.0.25_linux_amd64`.
- [ ] GREEN24 hardware behaviors remain unchanged: hard/soft/none seed
      semantics, hard-seed fail-away/fail-back, DNS retry/backoff,
      identity-collision protection, restarted-peer handling, route aging and
      RTMP relearning.

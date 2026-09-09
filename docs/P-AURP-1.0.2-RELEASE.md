# P-AURP 1.0.2 release notes

P-AURP **v1.0.2** is a focused interoperability patch release for the P-AURP
1.0 line. It preserves the field-proven v1.0.1 routing/data-plane behavior and
adds one narrowly scoped NBP compatibility normalization required for older
AURP/jrouter peers.

## Fixed: checksummed NBP Forward Requests across AURP

Some AppleTalk implementations, including the Debian Jessie-era Netatalk NBP
tools used in field testing, send routed NBP Forward Requests (FwdReq) with a
non-zero DDP checksum.

An AURP router receiving a FwdReq normally converts it into a local NBP LkUp by
changing DDP destination fields and the NBP function before broadcasting it on
the destination AppleTalk cable. An older router that preserves the original
non-zero checksum during this conversion produces a stale checksum. End nodes
can then reject or ignore the otherwise valid lookup.

P-AURP v1.0.2 normalizes this compatibility case at the AURP egress boundary:

- only DDP packets with protocol NBP are examined;
- only NBP FunctionFwdReq packets are eligible;
- only non-zero DDP checksums are changed;
- the packet is cloned so the original packet object is not mutated;
- the outbound AURP copy receives DDP checksum 0;
- zero-checksum FwdReqs, other NBP functions, malformed NBP and non-NBP DDP
  packets are left unchanged.

A DDP checksum value of zero means no checksum and remains valid after the
remote router rewrites the FwdReq into a local LkUp.

## Field proof

The failure was reproduced on a Windows P-AURP router with Linux Netatalk on a
separate bridged NIC path and an older remote jrouter peer serving the `btr`
zone.

Requester:

```text
DDP source:       1004.82.2
NBP requester:    1004.82.223
NBP query:        =:AFPServer@btr
Original checksum 0x44e9 on the request to network 2137
```

Before normalization:

```text
nbplkup '=:AFPServer@btr'
<no AFPServer result>
```

The same Linux host could successfully discover `Babylon 5` in `BabCom`, and
could discover the old BTR router itself, isolating the failure to remote NBP
FwdReq-to-LkUp handling rather than Linux NBP generation, P-AURP routing,
AURP return traffic, or AFP.

With the diagnostic checksum normalization enabled, the unchanged Linux lookup
immediately returned:

```text
Cloudberry:AFPServer  2137.47:129
```

This result is the field acceptance criterion retained for v1.0.2.

## Regression coverage

The v1.0.2 test suite includes dedicated coverage proving:

1. the field-shaped non-zero-checksum FwdReq is cloned and normalized to zero;
2. its DDP addressing and NBP query/requester tuple are preserved;
3. the original packet checksum is not mutated;
4. an already-zero FwdReq is unchanged;
5. an ordinary NBP LkUp is unchanged;
6. a non-NBP DDP packet is unchanged;
7. malformed NBP is unchanged.

The normal P-AURP release gates also remain required: global gofmt, go vet,
full unit tests, router/AURP/ZIP/status race tests, AURP stress tests, Windows
Npcap mapping, EtherTalk node-zero behavior, AEP checksum regression, Linux and
Windows amd64 builds, executable identity checks, and SHA-256 generation.

## Release branch

```text
release/p-aurp-v1.0.2-20260909
```

The exact release commit is recorded after all CI gates pass and is mirrored to
the dedicated release lock ref.

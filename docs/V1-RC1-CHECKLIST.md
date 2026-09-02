# jrouter v1.0.0-rc1 Soak Checklist

The v1.0.0-rc1 branch is cut from the final green Set28 pre-RC code point.
The version/workflow/helper commit contains no AURP protocol implementation
changes.

## CI gate

RC1 must pass all of the following at the exact artifact SHA:

- repository-wide gofmt;
- Go vet;
- full unit test suite;
- race tests for router, AURP, ZIP, and status;
- Phase 2/AURP stress suite repeated 100 times;
- full package build;
- Linux amd64 exact-SHA build;
- runtime `-version` proof showing `v1.0.0-rc1` and the exact commit SHA;
- SHA-256 artifact proof.

## Field soak

Use the ordinary production configuration first. Do not enable optional policy
features solely for the soak.

Watch for:

- unchanged PID and bounded goroutine count;
- receive queue returning to zero and remaining bounded;
- no unexpected loop-disabled peers;
- stable route/zone ownership through peer churn;
- correct receiver/sender asymmetry for peers that only establish one side;
- recovery after DNS endpoint changes and ordinary GlobalTalk outages;
- no accelerating ignored-ZI count from a stable peer;
- no unexplained reflection drops or loop indications;
- health/readiness and `/api/v1/aurp` remaining coherent;
- exact `1.0.0-rc1` / build-SHA provenance.

A 24-hour soak is the minimum useful RC observation. Forty-eight hours provides
better confidence for peer churn and DNS/reconnect behavior.

## Release blockers

Do not promote RC1 to v1.0.0 if the soak shows route leakage, stale forwarding
after peer loss, sustained receive-queue growth, connection-state deadlock,
routing-loop false positives, zone ownership corruption, or loss of exact build
provenance.

Optional features documented as intentionally unsupported are not release
blockers unless they affect the default AURP/IP interoperability path.

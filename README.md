# jrouter

Home-grown alternative implementation of Apple Internet Router 3.0

## Goals

* Full compatibility with Apple Internet Router 3.0
* Function on modern operating systems
* EtherTalk support

TashTalk could be a stretch goal, if I can acquire one!

## Caveats

Things I plan to fix Real Soon Now:

* ✅ Fixed ~~It currently listens to all AppleTalk and AARP traffic on the EtherTalk port.
  This might not play well with other AppleTalk software, e.g. netatalk.~~
* ✅ Fixed ~~Also it currently uses the default Ethernet address for the interface for
  sending packets. I plan to add the ability to configure a different address.~~ 
  You can now configure a different Ethernet address for the EtherTalk
  interface. I haven't tested it with netatalk or tashrouter on the same
  host, but I think using a distinct Ethernet address would help them coexist.
* It doesn't do any of the required packet splitting to keep packets under the
  AppleTalk size limits. ~~In particular ZIP GetZoneList Replies are incorrect
  when the zone list would exceed the limit.~~ GetZoneList is now fixed, but
  various others need fixing.
* It logs a lot and has no other monitoring or observability capability. I plan
  to add a Prometheus metrics endpoint and at least add log levels / verbosity
  flag.
* The AURP implementation is strictly incomplete, and lost connections with
  configured peers aren't re-established after some backoff. This won't be too
  difficult.

Things I plan to fix At Some Point:

* For expediency I made it act as a _seed router_. At some point I might add 
  "soft seed" functionality.


## How to use

WARNING: It Barely Works™

First, set up a `jrouter.yaml` (use the one in this repo as an example).

TODO: explain the configuration file

Building and running:

```shell
sudo apt install libpcap-dev
go install gitea.drjosh.dev/josh/jrouter@latest
sudo setcap 'CAP_NET_BIND_SERVICE=ep CAP_NET_RAW=ep' ~/go/bin/jrouter
~/go/bin/jrouter
```

* `NET_BIND_SERVICE` is needed to bind UDP port 387 (for talking between AIRs)
* `NET_RAW` is needed for EtherTalk

TODO: instructions for non-Linux machines

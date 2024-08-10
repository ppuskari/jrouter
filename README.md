# jrouter

Home-grown alternative implementation of Apple Internet Router 3.0

## Goals

* Full compatibility with Apple Internet Router 3.0
* Function on modern operating systems
* EtherTalk support
* Be observable (there's a HTTP server with a `/status` page)

TashTalk could be a stretch goal, if I can acquire one!

## Things that used to be caveats

* Previously it would listen for all EtherTalk traffic, regardless of destination.
  Now it doesn't do that, which should help it co-exist with other routers on
  the same host.
* You can configure an alternate Ethernet address if you are reusing the same
  network interface for multiple different EtherTalk software.
* In addition to the configured EtherTalk network and zone, it now learns routes
  and zones from other EtherTalk routers, and should share them across AURP.
* There's a status server. Browse to http://\[your router\]:9459/status to see
  information about the state of jrouter.

## Caveats

Things I plan to fix Real Soon Now:

* Some packet types need splitting to fit within limits. Some of these aren't
  implemented yet (mainly encapsulated). The unimplemented ones seem unlikely to
  hit those limits unless you are running a lot of routers or zones locally.
* I plan to add a Prometheus metrics endpoint and at least add log levels /
  verbosity config.
* The AURP implementation is mostly there, but not fully complete. The main
  thing missing is sequence number checking.

Things I plan to fix At Some Point:

* For expediency I made it act as a _seed router_. At some point I might add 
  "soft seed" functionality.


## How to use

WARNING: It Sorta Works™

First, set up a `jrouter.yaml` (use the one in this repo as an example).

TODO: explain the configuration file

Building and running:

1. Install [Go](https://go.dev/dl).

2. Run these commands (for Debian-variety Linuxen, e.g. Ubuntu, Raspbian, Mint...):

  ```shell
  sudo apt install git build-essential libpcap-dev
  go install gitea.drjosh.dev/josh/jrouter@latest
  sudo setcap 'CAP_NET_BIND_SERVICE=ep CAP_NET_RAW=ep' ~/go/bin/jrouter
  ```

3. Configure `jrouter.yaml`

4. To run:
  ```shell
  ~/go/bin/jrouter
  ```

Notes:

* `build-essential` is needed for `CGo`
* `libpcap-dev` and `CGo` is needed for [gopacket](https://github.com/google/gopacket)
* `NET_BIND_SERVICE` is needed to bind UDP port 387 (for talking between AIRs)
* `NET_RAW` is needed for EtherTalk

TODO: instructions for non-Linux / non-Debian-like machines

## Bug reports? Feature requests? Complaints? Praise? 

You can contact me on the Fediverse at @DrJosh9000@cloudisland.nz, or email me at josh.deprez@gmail.com. 

# jrouter

Home-grown alternative implementation of Apple Internet Router 3.0

## Goals

* Full compatibility with Apple Internet Router 3.0
* Function on modern operating systems
* EtherTalk support
* Be observable (there's a HTTP server with a `/status` page)
* (Stretch goal) TashTalk support

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

### Running with Docker

There is a container image available at `gitea.drjosh.dev/josh/jrouter:latest`.

Example `docker run` command:

```shell
# Run using a config file ./cfg/jrouter.yaml
docker run \
  -v ./cfg:/etc/jrouter \
  --cap-add NET_RAW \
  --net host \
  --name jrouter \
  gitea.drjosh.dev/josh/jrouter:latest
```

Notes:

* Put `jrouter.yaml` inside a `cfg` directory (or some path of your choice and bind-mount it at `/etc/jrouter`) for it to find the config file.
* `--cap-add NET_RAW` and `--net host` is needed for EtherTalk access to the network interface.
* By using `--net host`, the default AURP port (387) will be bound without `-p`.

### Building and running directly

1. Install [Go](https://go.dev/dl).
2. Run these commands (for Debian-variety Linuxen, e.g. Ubuntu, Raspbian, Mint...):
  ```shell
  sudo apt install git build-essential libpcap-dev
  go install drjosh.dev/jrouter@latest
  sudo setcap 'CAP_NET_BIND_SERVICE=ep CAP_NET_RAW=ep' ~/go/bin/jrouter
  ```
3. Configure `jrouter.yaml`
4. To run:
  ```shell
  ~/go/bin/jrouter
  ```

Notes:

* `git` is needed for `go install` to fetch the module
* `build-essential` and`libpcap-dev` are needed for [gopacket](https://github.com/google/gopacket), which uses [CGo](https://pkg.go.dev/cmd/cgo)
* `NET_BIND_SERVICE` is needed for `jrouter` to bind UDP port 387 (for talking between AIRs)
* `NET_RAW` is needed for `jrouter` to listen for and send EtherTalk packets

TODO: instructions for non-Linux / non-Debian-like machines

### Building and running with Docker manually

1.  Clone the repo and `cd` into it.
2.  `docker build -t jrouter .`
3.  Example `docker run` command:
    ```shell
    docker run \
      -v ./cfg:/etc/jrouter \
      --cap-add NET_RAW \
      --net host \
      --name jrouter \
      jrouter
    ```

Notes:

* Put `jrouter.yaml` inside a `cfg` directory (or some path of your choice and bind-mount it at `/etc/jrouter`) for it to find the config file.
* `--cap-add NET_RAW` and `--net host` is needed for EtherTalk access to the network interface.
* By using `--net host`, the default AURP port (387) will be bound without `-p`.

## Bug reports? Feature requests? Complaints? Praise?

You can contact me on the Fediverse at @DrJosh9000@cloudisland.nz, or email me at josh.deprez@gmail.com.

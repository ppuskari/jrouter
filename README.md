# jrouter

Home-grown alternative implementation of Apple Internet Router 3.0

## Goals

* Full compatibility with Apple Internet Router 3.0
* Function on modern operating systems
* EtherTalk support
* Be observable (there's a HTTP server with `/status` and `/metrics` pages)
* (Stretch goal) TashTalk support

## Things that used to be caveats

* Previously it would listen for all EtherTalk traffic, regardless of destination.
  Now it doesn't do that, which should help it co-exist with other routers on
  the same host.
* You can configure an alternate Ethernet address if you are reusing the same
  network interface for multiple different EtherTalk software.
* In addition to the configured EtherTalk network and zone, it now learns routes
  and zones from other EtherTalk routers, and should share them across AURP.
* There's a status endpoint that outputs diagnostic information about the state
  of the server. Set the `monitoring_addr` config option and then browse to
  `http://[your router]:[port you configured]/status` to see information about
  the state of jrouter.

## Caveats & known bugs

* For expediency I made it act as a _seed router_ only. I hope to add "non-seed"
  and "soft-seed" mode soon!
* I have not yet tested with `netatalk` on the same host. I have seen reports
  that it is (at best) very flaky (zones appearing and disappearing). For now I
  recommend running `jrouter` and `netatalk` on separate hosts.
* Some packet types aren't currently split correctly to fit within limits. This
  mainly affects routers with lots of distinct routes in the local EtherTalk
  network.
* The AURP implementation is about 95% complete. The main thing missing is
  sequence number checking.

The issues in this repo should be updated as things get fixed.

## How to use

WARNING: It Sorta Works™. See "Caveats & known bugs" above.

First, write a `jrouter.yaml` config file.
Use [the jrouter.yaml in this repo](/josh/jrouter/src/branch/main/jrouter.yaml)
as both an example and for documentation of config options.

Then choose from the options below:

### Running with Docker

Multiarch (x86_64 and arm64) container images are available from this server.

* `gitea.drjosh.dev/josh/jrouter:latest` - latest release version
* `gitea.drjosh.dev/josh/jrouter:0.0.12` - specific patch version
* `gitea.drjosh.dev/josh/jrouter:0.0` - latest patch release for minor version
* `gitea.drjosh.dev/josh/jrouter:0` - latest minor & patch release for major version
* `gitea.drjosh.dev/josh/jrouter:dev` - pre-release that I'm currently testing

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

### Docker Compose

Example `docker-compose.yml` file:

```yaml
services:
  jrouter:
    image: gitea.drjosh.dev/josh/jrouter:latest
    restart: unless-stopped
    volumes:
      - type: bind
        source: ./jrouter
        target: /etc/jrouter
    network_mode: host
    cap_add:
      - NET_RAW
```

### Building and running directly

1. Install [Go](https://go.dev/dl).
2. Run these commands (for Debian-variety Linuxen, e.g. Ubuntu, Raspbian, Mint...):
  ```shell
  sudo apt install git build-essential libpcap-dev
  go install drjosh.dev/jrouter@latest   # or substitute @latest with @(version) e.g. @v0.0.12
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
* By default `jrouter` looks for `jrouter` in the current directory. It can be
  changed with the `config` flag:
  ```shell
  jrouter -config /etc/jrouter/jrouter.yaml
  ```

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

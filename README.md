# jrouter

Home-grown alternative implementation of Apple Internet Router 3.0

## Goals

* Full compatibility with Apple Internet Router 3.0
* Function on modern operating systems
* EtherTalk support

TashTalk could be a stretch goal, if I can acquire one!

## How to use

WARNING: It Barely Works™

First, set up a `jrouter.yaml` (use the one in this repo as an example).

TODO: explain the configuration file

Building and running:

```shell
go install gitea.drjosh.dev/josh/jrouter@latest
sudo setcap 'CAP_NET_BIND_SERVICE=ep CAP_NET_RAW=ep' ~/go/bin/jrouter
~/go/bin/jrouter
```

* `NET_BIND_SERVICE` is needed to bind UDP port 387 (for talking between AIRs)
* `NET_RAW` is needed for EtherTalk

TODO: instructions for non-Linux machines

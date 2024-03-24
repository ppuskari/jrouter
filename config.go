package main

import (
	"os"

	"gopkg.in/yaml.v3"
)

type config struct {
	// Optional: default is 387.
	ListenPort uint16 `yaml:"listen_port"`

	// Sets the Domain Identifier used by this router.
	// Note: this does not "bind" the IP side of the router to a particular
	// interface; it will listen on all interfaces with IP addresses.
	// Optional: defaults to the first global unicast address on any local
	// network interface.
	LocalIP string `yaml:"local_ip"`

	// Required for routing a local EtherTalk network.
	EtherTalk struct {
		ZoneName string `yaml:"zone_name"`
		NetStart uint16 `yaml:"net_start"`
		NetEnd   uint16 `yaml:"net_end"`
	} `yaml:"ethertalk"`

	// LocalTalk struct {
	//	ZoneName   string `yaml:"zone_name"`
	// 	Network uint16 `yaml:"network"`
	// } `yaml:"localtalk"`

	// Allow routers other than those listed under peers?
	OpenPeering bool `yaml:"open_peering"`

	// List of peer routers.
	Peers []string `yaml:"peers"`
}

func loadConfig(cfgPath string) (*config, error) {
	f, err := os.Open(cfgPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	c := new(config)
	if err := yaml.NewDecoder(f).Decode(c); err != nil {
		return nil, err
	}

	if c.ListenPort == 0 {
		c.ListenPort = 387
	}

	return c, nil
}

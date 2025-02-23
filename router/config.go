/*
   Copyright 2024 Josh Deprez

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package router

import (
	"fmt"
	"os"

	"github.com/sfiera/multitalk/pkg/ddp"
	"gopkg.in/yaml.v3"
)

type Config struct {
	// ListenPort is the AURP service port. Optional: default is 387.
	ListenPort uint16 `yaml:"listen_port"`

	// MonitoringAddr is used for hosting /status server and /metrics.
	// Example: ":9459" (listen on port 9459 on all interfaces).
	// Optional: when left empty, the monitoring HTTP server is disabled.
	MonitoringAddr string `yaml:"monitoring_addr"`

	// LocalIP configures the Domain Identifier used by this router.
	// Note: this does not "bind" the IP side of the router to a particular
	// interface; it will listen on all interfaces with IP addresses.
	// Optional: defaults to the first global unicast address on any local
	// network interface.
	LocalIP string `yaml:"local_ip"`

	// EtherTalk is required for routing one or more local EtherTalk networks.
	EtherTalk EtherTalkConfigs `yaml:"ethertalk"`

	// LocalTalk is TODO.
	// LocalTalk struct {
	//	ZoneName   string `yaml:"zone_name"`
	// 	Network uint16 `yaml:"network"`
	// } `yaml:"localtalk"`

	// OpenPeering allowsrouters other than those listed under peers.
	OpenPeering bool `yaml:"open_peering"`

	// Peers sets a list of peer routers to connect to and allow connections
	// from.
	Peers []string `yaml:"peers"`

	// PeerListURL sets a URL to fetch a list of peers from (plain text, one
	// peer per line).
	PeerListURL string `yaml:"peerlist_url"`
}

type EtherTalkConfigs []*EtherTalkConfig

func (cs *EtherTalkConfigs) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		return n.Decode((*[]*EtherTalkConfig)(cs))

	case yaml.MappingNode:
		var v EtherTalkConfig
		if err := n.Decode(&v); err != nil {
			return err
		}
		*cs = append(*cs, &v)
		return nil

	default:
		return fmt.Errorf("invalid YAML kind for 'ethertalk' %v, want either a sequence or a mapping", n.Kind)
	}
}

// EtherTalkConfig configures EtherTalk for a specific Ethernet interface.
type EtherTalkConfig struct {
	// EthAddr overrides the hardware address used by jrouter. Optional.
	EthAddr string `yaml:"ethernet_addr"`

	// Device is the Ethernet device name (e.g. eth0, enp2s0, en3). Required.
	Device string `yaml:"device"`

	// ZoneName is the AppleTalk zone name for the network on this interface.
	// Required.
	ZoneName string `yaml:"zone_name"`

	// NetStart and NetEnd control the network number range for the AppleTalk
	// network on this interface (inclusive). Required.
	NetStart ddp.Network `yaml:"net_start"`
	NetEnd   ddp.Network `yaml:"net_end"`
}

func LoadConfig(cfgPath string) (*Config, error) {
	f, err := os.Open(cfgPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	c := new(Config)
	if err := yaml.NewDecoder(f).Decode(c); err != nil {
		return nil, err
	}

	if c.ListenPort == 0 {
		c.ListenPort = 387
	}

	return c, nil
}

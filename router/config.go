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
	"errors"
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

	// TODO: ExtraAdvertisedZones is a set of extra zones that are not managed by
	// jouter but that can be advertised over AURP if a valid route becomes
	// available through the local EtherTalk (e.g. from a neighbouring netatalk
	// router).
	// ExtraAdvertisedZones []string `yaml:"extra_advertised_zones"`

	// TODO HiddenZones prevents zones from being advertised over AURP.
	// HiddenZones []string `yaml:"hidden_zones"`

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

	// DefaultZoneName is the AppleTalk zone name for the network on this
	// interface. Required.
	DefaultZoneName string `yaml:"zone_name"`

	// ExtraZones is a list of any additional zone names that are available
	// within this local network. Nodes can choose from the default zone name
	// or any of these additional names.
	ExtraZones []string `yaml:"extra_zones"`

	// NetStart and NetEnd control the network number range for the AppleTalk
	// network on this interface (inclusive). Required.
	NetStart ddp.Network `yaml:"net_start"`
	NetEnd   ddp.Network `yaml:"net_end"`
}

// LoadConfig readand parses a configuration file, and sets some defaults.
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

	// Default to AURP listening port 387
	if c.ListenPort == 0 {
		c.ListenPort = 387
	}

	var validationErrs []error

	// Check zone names
	for _, port := range c.EtherTalk {
		// 255 is the limit on available zones for a network.
		if zoneCount := len(port.ExtraZones) + 1; zoneCount > 255 {
			validationErrs = append(validationErrs, fmt.Errorf("too many zones (%d > 255) for port %q", zoneCount, port.Device))
		}
		// Must be 32 characters or fewer.
		if len(port.DefaultZoneName) > 32 {
			validationErrs = append(validationErrs, fmt.Errorf("port %q zone name %q (length %d) is too long; cannot be more than 32 characters", port.Device, port.DefaultZoneName, len(port.DefaultZoneName)))
		}
		// Must not be empty or '*'
		if port.DefaultZoneName == "" || port.DefaultZoneName == "*" {
			validationErrs = append(validationErrs, fmt.Errorf("port %q zone name %q is invalid; cannot be empty or *", port.Device, port.DefaultZoneName))
		}
		// The above, but for all extra zones
		for _, zn := range port.ExtraZones {
			if len(zn) > 32 {
				validationErrs = append(validationErrs, fmt.Errorf("port %q extra zone name %q (length %d) is too long; cannot be more than 32 characters", port.Device, zn, len(zn)))
			}
			if zn == "" || zn == "*" {
				validationErrs = append(validationErrs, fmt.Errorf("port %q zone name %q is invalid; cannot be empty or *", port.Device, port.DefaultZoneName))
			}
		}
	}

	// Note [errors.Join] here does the right thing if validationErrs is empty
	return c, errors.Join(validationErrs...)
}

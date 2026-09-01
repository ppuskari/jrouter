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
	"strconv"
	"strings"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
	"go.yaml.in/yaml/v3"
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

	// OpenPeering allows routers other than those listed under peers.
	OpenPeering bool `yaml:"open_peering"`

	// AURP controls protocol liveness and retry timing. All fields are optional
	// and default to the historical jrouter values when omitted.
	AURP AURPConfig `yaml:"aurp"`

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

type YAMLDuration time.Duration

func (d *YAMLDuration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar, got YAML kind %v", n.Kind)
	}
	if n.Tag == "!!int" {
		seconds, err := strconv.ParseInt(n.Value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid duration seconds %q: %w", n.Value, err)
		}
		*d = YAMLDuration(time.Duration(seconds) * time.Second)
		return nil
	}
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	*d = YAMLDuration(v)
	return nil
}

type AURPNetworkRange struct {
	Start ddp.Network
	End   ddp.Network
}

func (r *AURPNetworkRange) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("AURP network range must be a scalar, got YAML kind %v", n.Kind)
	}
	parts := strings.SplitN(strings.TrimSpace(n.Value), "-", 2)
	parse := func(s string) (ddp.Network, error) {
		v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
		if err != nil {
			return 0, fmt.Errorf("invalid AppleTalk network %q: %w", s, err)
		}
		network := ddp.Network(v)
		if network == 0 || network >= 0xff00 {
			return 0, fmt.Errorf("AppleTalk network %d is not valid for AURP hiding", network)
		}
		return network, nil
	}
	start, err := parse(parts[0])
	if err != nil {
		return err
	}
	end := start
	if len(parts) == 2 {
		end, err = parse(parts[1])
		if err != nil {
			return err
		}
	}
	if start > end {
		return fmt.Errorf("AURP hidden network range is reversed (%d-%d)", start, end)
	}
	*r = AURPNetworkRange{Start: start, End: end}
	return nil
}

func (r AURPNetworkRange) contains(network ddp.Network) bool {
	return network >= r.Start && network <= r.End
}

func (r AURPNetworkRange) overlaps(start, end ddp.Network) bool {
	return start <= r.End && end >= r.Start
}

type AURPConfig struct {
	LastHeardFromTimeout  time.Duration      `yaml:"-"`
	RetryInterval         time.Duration      `yaml:"-"`
	SendRetryLimit        int                `yaml:"send_retry_limit"`
	TickleRetryLimit      int                `yaml:"tickle_retry_limit"`
	ZoneInfoRetryInterval time.Duration      `yaml:"-"`
	HiddenNetworks        []AURPNetworkRange `yaml:"hidden_networks"`
	HopCountReduction     bool               `yaml:"hop_count_reduction"`
	HopCountWeight        uint8              `yaml:"hop_count_weight"`
}

func (c AURPConfig) networkHidden(network ddp.Network) bool {
	for _, r := range c.HiddenNetworks {
		if r.contains(network) {
			return true
		}
	}
	return false
}

func (c AURPConfig) rangeHidden(start, end ddp.Network) bool {
	for _, r := range c.HiddenNetworks {
		if r.overlaps(start, end) {
			return true
		}
	}
	return false
}

func (c *AURPConfig) UnmarshalYAML(n *yaml.Node) error {
	var raw struct {
		LastHeardFromTimeout  YAMLDuration       `yaml:"last_heard_from_timeout"`
		RetryInterval         YAMLDuration       `yaml:"retry_interval"`
		SendRetryLimit        int                `yaml:"send_retry_limit"`
		TickleRetryLimit      int                `yaml:"tickle_retry_limit"`
		ZoneInfoRetryInterval YAMLDuration       `yaml:"zone_info_retry_interval"`
		HiddenNetworks        []AURPNetworkRange `yaml:"hidden_networks"`
		HopCountReduction     bool               `yaml:"hop_count_reduction"`
		HopCountWeight        uint8              `yaml:"hop_count_weight"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	*c = AURPConfig{
		LastHeardFromTimeout:  time.Duration(raw.LastHeardFromTimeout),
		RetryInterval:         time.Duration(raw.RetryInterval),
		SendRetryLimit:        raw.SendRetryLimit,
		TickleRetryLimit:      raw.TickleRetryLimit,
		ZoneInfoRetryInterval: time.Duration(raw.ZoneInfoRetryInterval),
		HiddenNetworks:        raw.HiddenNetworks,
		HopCountReduction:     raw.HopCountReduction,
		HopCountWeight:        raw.HopCountWeight,
	}
	return nil
}

func (c *AURPConfig) applyDefaults() {
	if c.LastHeardFromTimeout == 0 {
		c.LastHeardFromTimeout = lastHeardFromTimer
	}
	if c.RetryInterval == 0 {
		c.RetryInterval = sendRetryTimer
	}
	if c.SendRetryLimit == 0 {
		c.SendRetryLimit = sendRetryLimit
	}
	if c.TickleRetryLimit == 0 {
		c.TickleRetryLimit = tickleRetryLimit
	}
	if c.ZoneInfoRetryInterval == 0 {
		c.ZoneInfoRetryInterval = aurpZoneInfoRetryTimer
	}
}

type SeedMode string

const (
	SeedModeHard SeedMode = "hard"
	SeedModeSoft SeedMode = "soft"
	SeedModeNone SeedMode = "none"
)

// EtherTalkConfig configures EtherTalk for a specific Ethernet interface.
type EtherTalkConfig struct {
	// SeedMode controls whether this interface seeds AppleTalk cable/zone
	// information. "hard" seeds immediately, "soft" first defers to any
	// existing seed router, and "none" never acts as a seed router.
	// Optional; defaults to "hard" for compatibility with historical jrouter.
	SeedMode SeedMode `yaml:"seed_mode"`

	// SoftSeedDelay is how long a soft seed waits for another router to
	// establish the configured cable range before promoting itself.
	// Optional; defaults to 30 seconds. Ignored for hard/none.
	SoftSeedDelay time.Duration `yaml:"soft_seed_delay"`

	// EthAddr overrides the hardware address used by jrouter. Optional.
	EthAddr string `yaml:"ethernet_addr"`

	// Device is the Ethernet device name (e.g. eth0, enp2s0, en3). Required.
	Device string `yaml:"device"`

	// DefaultZoneName is the AppleTalk zone name for the network on this
	// interface. Required for hard/soft seeds. A true non-seed may leave it
	// empty and discover the default zone through ZIP GetNetInfo.
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

func (c *EtherTalkConfig) UnmarshalYAML(n *yaml.Node) error {
	var raw struct {
		SeedMode        SeedMode     `yaml:"seed_mode"`
		SoftSeedDelay   YAMLDuration `yaml:"soft_seed_delay"`
		EthAddr         string       `yaml:"ethernet_addr"`
		Device          string       `yaml:"device"`
		DefaultZoneName string       `yaml:"zone_name"`
		ExtraZones      []string     `yaml:"extra_zones"`
		NetStart        ddp.Network  `yaml:"net_start"`
		NetEnd          ddp.Network  `yaml:"net_end"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	*c = EtherTalkConfig{
		SeedMode:        raw.SeedMode,
		SoftSeedDelay:   time.Duration(raw.SoftSeedDelay),
		EthAddr:         raw.EthAddr,
		Device:          raw.Device,
		DefaultZoneName: raw.DefaultZoneName,
		ExtraZones:      raw.ExtraZones,
		NetStart:        raw.NetStart,
		NetEnd:          raw.NetEnd,
	}
	return nil
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
	c.AURP.applyDefaults()

	var validationErrs []error
	if c.AURP.LastHeardFromTimeout < 30*time.Second {
		validationErrs = append(validationErrs, fmt.Errorf(
			"aurp.last_heard_from_timeout must be at least 30s, got %v",
			c.AURP.LastHeardFromTimeout,
		))
	}
	if c.AURP.RetryInterval <= 0 {
		validationErrs = append(validationErrs, fmt.Errorf(
			"aurp.retry_interval must be positive, got %v", c.AURP.RetryInterval,
		))
	}
	if c.AURP.SendRetryLimit <= 0 {
		validationErrs = append(validationErrs, fmt.Errorf(
			"aurp.send_retry_limit must be positive, got %d", c.AURP.SendRetryLimit,
		))
	}
	if c.AURP.TickleRetryLimit <= 0 {
		validationErrs = append(validationErrs, fmt.Errorf(
			"aurp.tickle_retry_limit must be positive, got %d", c.AURP.TickleRetryLimit,
		))
	}
	if c.AURP.ZoneInfoRetryInterval <= 0 {
		validationErrs = append(validationErrs, fmt.Errorf(
			"aurp.zone_info_retry_interval must be positive, got %v", c.AURP.ZoneInfoRetryInterval,
		))
	}
	if c.AURP.HopCountWeight >= maxRouteDistance {
		validationErrs = append(validationErrs, fmt.Errorf(
			"aurp.hop_count_weight must be less than %d, got %d",
			maxRouteDistance,
			c.AURP.HopCountWeight,
		))
	}

	// Check EtherTalk port configuration.
	for _, port := range c.EtherTalk {
		if port.SeedMode == "" {
			port.SeedMode = SeedModeHard
		}
		port.SeedMode = SeedMode(strings.ToLower(string(port.SeedMode)))
		switch port.SeedMode {
		case SeedModeHard, SeedModeSoft, SeedModeNone:
		default:
			validationErrs = append(validationErrs, fmt.Errorf(
				"invalid seed_mode %q for port %q; want hard, soft, or none",
				port.SeedMode, port.Device,
			))
		}
		if port.SoftSeedDelay == 0 {
			port.SoftSeedDelay = 30 * time.Second
		}
		if port.SoftSeedDelay < 0 {
			validationErrs = append(validationErrs, fmt.Errorf(
				"soft_seed_delay for port %q must not be negative",
				port.Device,
			))
		}
		// Hard and soft seeds need a configured cable range. A true non-seed
		// may omit it and discover/adopt the cable range dynamically while
		// using the AppleTalk Phase 2 startup range.
		hasNetStart := port.NetStart != 0
		hasNetEnd := port.NetEnd != 0
		if hasNetStart != hasNetEnd {
			validationErrs = append(validationErrs, fmt.Errorf(
				"port %q must configure both net_start and net_end, or neither",
				port.Device,
			))
		}
		if port.SeedMode != SeedModeNone && !hasNetStart && !hasNetEnd {
			validationErrs = append(validationErrs, fmt.Errorf(
				"seed_mode %q on port %q requires net_start and net_end",
				port.SeedMode, port.Device,
			))
		}
		if hasNetStart && hasNetEnd {
			// Invalid configured network numbers are:
			//
			// 	0x0000 (0) - unknown / local network
			// 	0xff00 - 0xfffe - Phase 2 startup range
			// 	0xffff (65535) - reserved/invalid
			if port.NetStart > port.NetEnd {
				validationErrs = append(validationErrs, fmt.Errorf("the network number range used for port %q is backwards (start %d > end %d)", port.Device, port.NetStart, port.NetEnd))
			}
			if port.NetStart == 0xffff || port.NetEnd == 0xffff {
				validationErrs = append(validationErrs, fmt.Errorf("invalid network number 65535 used for port %q", port.Device))
			}
			if (port.NetStart >= 0xff00 && port.NetStart <= 0xfffe) || (port.NetEnd >= 0xff00 && port.NetEnd <= 0xfffe) {
				validationErrs = append(validationErrs, fmt.Errorf("invalid network number range (%d - %d) used for port %q; it must not overlap the startup range (65280 - 65534)", port.NetStart, port.NetEnd, port.Device))
			}
		}

		// 255 is the limit on available zones for a network.
		if zoneCount := len(port.ExtraZones) + 1; zoneCount > 255 {
			validationErrs = append(validationErrs, fmt.Errorf("too many zones (%d > 255) for port %q", zoneCount, port.Device))
		}
		// Must be 32 characters or fewer.
		if len(port.DefaultZoneName) > 32 {
			validationErrs = append(validationErrs, fmt.Errorf("port %q zone name %q (length %d) is too long; cannot be more than 32 characters", port.Device, port.DefaultZoneName, len(port.DefaultZoneName)))
		}
		// Hard/soft seeds must have an explicit default zone. A true
		// non-seed may send a NIL zone in GetNetInfo and learn the cable's
		// default zone from the reply.
		if port.DefaultZoneName == "*" ||
			(port.SeedMode != SeedModeNone && port.DefaultZoneName == "") {
			validationErrs = append(validationErrs, fmt.Errorf(
				"port %q zone name %q is invalid for seed_mode %q",
				port.Device, port.DefaultZoneName, port.SeedMode,
			))
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

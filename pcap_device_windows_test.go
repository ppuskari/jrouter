//go:build windows

package main

import (
	"net"
	"strings"
	"testing"

	"github.com/google/gopacket/pcap"
)

func TestPcapDeviceNameMatchesNpcap(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	devs, err := pcap.FindAllDevs()
	if err != nil {
		t.Fatal(err)
	}

	pcapNames := make(map[string]struct{}, len(devs))
	for _, dev := range devs {
		pcapNames[dev.Name] = struct{}{}
	}

	matched := 0
	for i := range ifaces {
		name, err := pcapDeviceName(&ifaces[i])
		if err != nil {
			continue
		}
		if !strings.HasPrefix(name, `\Device\NPF_`) {
			t.Errorf("interface %q mapped to unexpected pcap name %q", ifaces[i].Name, name)
			continue
		}
		if _, ok := pcapNames[name]; ok {
			matched++
		}
	}

	if matched == 0 {
		t.Fatalf("no Windows interfaces mapped to an Npcap capture device")
	}
}

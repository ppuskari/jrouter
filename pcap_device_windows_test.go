//go:build windows

package main

import (
	"net"
	"strings"
	"testing"
)

func TestPcapDeviceNameWindows(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}

	mapped := 0
	for i := range ifaces {
		name, err := pcapDeviceName(&ifaces[i])
		if err != nil {
			continue
		}
		if !strings.HasPrefix(name, `\Device\NPF_`) {
			t.Errorf("interface %q mapped to unexpected pcap name %q", ifaces[i].Name, name)
			continue
		}
		if len(strings.TrimPrefix(name, `\Device\NPF_`)) == 0 {
			t.Errorf("interface %q mapped to an empty Npcap adapter name", ifaces[i].Name)
			continue
		}
		mapped++
	}

	if mapped == 0 {
		t.Fatalf("no Windows interfaces could be mapped to an Npcap device path")
	}
}

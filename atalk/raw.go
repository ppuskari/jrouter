package atalk

import (
	"fmt"

	"github.com/google/gopacket/pcap"
)

// StartPcap opens an AppleTalk and AARP listening session on a network device.
func StartPcap(device string) (*pcap.Handle, error) {
	handle, err := pcap.OpenLive(device, 4096, true, pcap.BlockForever)
	if err != nil {
		return nil, fmt.Errorf("opening device %q: %w", device, err)
	}

	if err := handle.SetBPFFilter("atalk or aarp"); err != nil {
		handle.Close()
		return nil, fmt.Errorf("setting BPF filter: %w", err)
	}

	return handle, nil
}

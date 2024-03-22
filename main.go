package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"log"
	"net"

	"gitea.drjosh.dev/josh/jrouter/aurp"
)

var localIPAddr = flag.String("local-ip", "", "IPv4 address to use as the Source Domain Identifier")

func main() {
	flag.Parse()
	log.Println("jrouter")

	localIP := net.ParseIP(*localIPAddr).To4()
	if localIP == nil {
		iaddrs, err := net.InterfaceAddrs()
		if err != nil {
			log.Fatalf("Couldn't read network interface addresses: %v", err)
		}
		for _, iaddr := range iaddrs {
			inet, ok := iaddr.(*net.IPNet)
			if !ok {
				continue
			}
			if !inet.IP.IsGlobalUnicast() {
				continue
			}
			localIP = inet.IP.To4()
			if localIP != nil {
				break
			}
		}
		if localIP == nil {
			log.Fatalf("No global unicast IPv4 addresses on any network interfaces, and no valid address passed with --local-ip")
		}
	}

	log.Printf("Using %v as local domain identifier", localIP)

	peers := make(map[uint32]*aurp.Transport)
	var nextConnID uint16

	ln, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 387})
	if err != nil {
		log.Fatalf("Couldn't listen on udp4:387: %v", err)
	}

	// Incoming packet loop
	pb := make([]byte, 65536)
	for {
		pktlen, raddr, readErr := ln.ReadFromUDP(pb)
		// net.PacketConn.ReadFrom: "Callers should always process
		// the n > 0 bytes returned before considering the error err."

		dh, _, err := aurp.ParseDomainHeader(pb[:pktlen])
		if err != nil {
			log.Printf("Failed to parse domain header: %v", err)
		}

		pkt, parseErr := aurp.ParsePacket(pb[:pktlen])
		if parseErr != nil {
			log.Printf("Failed to parse packet: %v", parseErr)
		}

		if readErr != nil {
			log.Printf("Failed to read packet: %v", readErr)
			continue
		}

		// Existing peer?
		rip := binary.BigEndian.Uint32(raddr.IP)
		tr := peers[rip]
		if tr == nil {
			// New peer!
			tr = &aurp.Transport{
				LocalDI:     aurp.IPDomainIdentifier(localIP),
				RemoteDI:    dh.SourceDI,
				LocalConnID: nextConnID,
			}
			nextConnID++
			peers[rip] = tr
		}

		switch p := pkt.(type) {
		case *aurp.AppleTalkPacket:
			// Probably something like:
			//
			// * parse the DDP header
			// * check that this is headed for our local network
			// * write the packet out in an EtherTalk frame
			//
			// or maybe if we were implementing a "central hub"
			//
			// * parse the DDP header
			// * see if we know the network
			// * forward to the peer with that network and lowest metric

		case *aurp.OpenReqPacket:
			// The peer tells us their connection ID in Open-Req.
			tr.RemoteConnID = p.ConnectionID

			// Formulate a response.
			var rp *aurp.OpenRspPacket
			if p.Version != 1 {
				// Respond with Open-Rsp with unknown version error.
				rp = tr.NewOpenRspPacket(0, aurp.ErrCodeInvalidVersion, nil)
			} else {
				// Accept the connection, I guess?
				rp = tr.NewOpenRspPacket(0, 1, nil)
			}

			// Write an Open-Rsp packet
			var b bytes.Buffer
			if _, err := rp.WriteTo(&b); err != nil {
				log.Printf("Couldn't create response packet: %v", err)
				break
			}
			if _, err := ln.WriteToUDP(b.Bytes(), raddr); err != nil {
				log.Printf("Couldn't write response packet to UDP peer %v: %v", raddr, err)
			}

		case *aurp.OpenRspPacket:
			if p.RateOrErrCode < 0 {
				// It's an error code.
				log.Printf("Open-Rsp error code from peer %v: %d", raddr.IP, p.RateOrErrCode)
			}

		}
	}
}

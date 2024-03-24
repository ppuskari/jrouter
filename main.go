package main

import (
	"bytes"
	"flag"
	"log"
	"net"
	"regexp"

	"gitea.drjosh.dev/josh/jrouter/aurp"
)

var hasPortRE = regexp.MustCompile(`:\d+$`)

var configFilePath = flag.String("config", "jrouter.yaml", "Path to configuration file to use")

type peer struct {
	tr    *aurp.Transport
	conn  *net.UDPConn
	raddr *net.UDPAddr
}

func (p *peer) dataReceiver() {
	// Write an Open-Req packet
	oreq := p.tr.NewOpenReqPacket(nil)
	var b bytes.Buffer
	if _, err := oreq.WriteTo(&b); err != nil {
		log.Printf("Couldn't write Open-Req packet to buffer: %v", err)
		return
	}
	n, err := p.conn.WriteToUDP(b.Bytes(), p.raddr)
	if err != nil {
		log.Printf("Couldn't write packet to peer: %v", err)
		return
	}
	log.Printf("Sent Open-Req (len %d) to peer %v", n, p.raddr)

}

func main() {
	flag.Parse()
	log.Println("jrouter")

	cfg, err := loadConfig(*configFilePath)
	if err != nil {
		log.Fatalf("Couldn't load configuration file: %v", err)
	}

	localIP := net.ParseIP(cfg.LocalIP).To4()
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
			log.Fatalf("No global unicast IPv4 addresses on any network interfaces, and no valid local_ip address in configuration")
		}
	}

	log.Printf("Using %v as local domain identifier", localIP)

	peers := make(map[udpAddr]*peer)
	var nextConnID uint16

	ln, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(cfg.ListenPort)})
	if err != nil {
		log.Fatalf("Couldn't listen on udp4:387: %v", err)
	}
	defer ln.Close()
	log.Printf("Listening on %v", ln.LocalAddr())

	for _, peerStr := range cfg.Peers {
		if !hasPortRE.MatchString(peerStr) {
			peerStr += ":387"
		}

		raddr, err := net.ResolveUDPAddr("udp4", peerStr)
		if err != nil {
			log.Fatalf("Invalid UDP address: %v", err)
		}
		log.Printf("resolved %q to %v", peerStr, raddr)

		tr := &aurp.Transport{
			LocalDI:     aurp.IPDomainIdentifier(localIP),
			RemoteDI:    aurp.IPDomainIdentifier(raddr.IP),
			LocalConnID: nextConnID,
		}
		nextConnID++

		// conn, err := net.DialUDP("udp4", nil, raddr)
		// if err != nil {
		// 	log.Printf("Couldn't dial %v->%v: %v", nil, raddr, err)
		// 	continue
		// }
		// log.Printf("conn.LocalAddr = %v", conn.LocalAddr())

		peer := &peer{
			tr:    tr,
			conn:  ln,
			raddr: raddr,
		}
		go peer.dataReceiver()

		peers[udpAddrFromNet(raddr)] = peer
	}

	// Incoming packet loop
	pb := make([]byte, 65536)
	for {
		pktlen, raddr, readErr := ln.ReadFromUDP(pb)
		// net.PacketConn.ReadFrom: "Callers should always process
		// the n > 0 bytes returned before considering the error err."

		log.Printf("Received packet of length %d from %v", pktlen, raddr)

		dh, _, parseErr := aurp.ParseDomainHeader(pb[:pktlen])
		if parseErr != nil {
			log.Printf("Failed to parse domain header: %v", err)
		}

		pkt, parseErr := aurp.ParsePacket(pb[:pktlen])
		if parseErr != nil {
			log.Printf("Failed to parse packet: %v", parseErr)
		}

		log.Printf("The packet parsed succesfully as a %T", pkt)

		if readErr != nil {
			log.Printf("Failed to read packet: %v", readErr)
			continue
		}

		// Existing peer?
		ra := udpAddrFromNet(raddr)
		pr := peers[ra]
		if pr == nil {
			// New peer!
			nextConnID++
			pr = &peer{
				tr: &aurp.Transport{
					LocalDI:     aurp.IPDomainIdentifier(localIP),
					RemoteDI:    dh.SourceDI, // platinum rule
					LocalConnID: nextConnID,
				},
				conn:  ln,
				raddr: raddr,
			}
			peers[ra] = pr
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
			pr.tr.RemoteConnID = p.ConnectionID

			// Formulate a response.
			var rp *aurp.OpenRspPacket
			switch {
			case p.Version != 1:
				// Respond with Open-Rsp with unknown version error.
				rp = pr.tr.NewOpenRspPacket(0, aurp.ErrCodeInvalidVersion, nil)

			case len(p.Options) > 0:
				// Options? OPTIONS? We don't accept no stinkin' _options_
				rp = pr.tr.NewOpenRspPacket(0, aurp.ErrCodeOptionNegotiation, nil)

			default:
				// Accept it I guess.
				rp = pr.tr.NewOpenRspPacket(0, 1, nil)
			}

			log.Printf("Responding with %T", rp)

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

			// TODO

		}
	}
}

// Hashable net.UDPAddr
type udpAddr struct {
	ipv4 [4]byte
	port uint16
}

func udpAddrFromNet(a *net.UDPAddr) udpAddr {
	return udpAddr{
		ipv4: [4]byte(a.IP.To4()),
		port: uint16(a.Port),
	}
}

func (u udpAddr) toNet() *net.UDPAddr {
	return &net.UDPAddr{
		IP:   u.ipv4[:],
		Port: int(u.port),
	}
}

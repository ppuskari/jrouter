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
	localDI := aurp.IPDomainIdentifier(localIP)

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
			LocalDI:     localDI,
			RemoteDI:    aurp.IPDomainIdentifier(raddr.IP),
			LocalConnID: nextConnID,
		}
		nextConnID++

		peer := &peer{
			tr:    tr,
			conn:  ln,
			raddr: raddr,
			recv:  make(chan aurp.Packet, 1024),
		}
		go peer.handle()

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
			continue
		}

		pkt, parseErr := aurp.ParsePacket(pb[:pktlen])
		if parseErr != nil {
			log.Printf("Failed to parse packet: %v", parseErr)
			continue
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
					LocalDI:     localDI,
					RemoteDI:    dh.SourceDI, // platinum rule
					LocalConnID: nextConnID,
				},
				conn:  ln,
				raddr: raddr,
				recv:  make(chan aurp.Packet, 1024),
			}
			peers[ra] = pr
			go pr.handle()
		}

		// Pass the packet to the goroutine in charge of this peer.
		pr.recv <- pkt
	}
}

type peer struct {
	tr    *aurp.Transport
	conn  *net.UDPConn
	raddr *net.UDPAddr
	recv  chan aurp.Packet
}

// send encodes and sends pkt to the remote host.
func (p *peer) send(pkt aurp.Packet) (int, error) {
	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		return 0, err
	}
	return p.conn.WriteToUDP(b.Bytes(), p.raddr)
}

func (p *peer) handle() {
	// Write an Open-Req packet
	n, err := p.send(p.tr.NewOpenReqPacket(nil))
	if err != nil {
		log.Printf("Couldn't send Open-Req packet: %v", err)
		return
	}
	log.Printf("Sent Open-Req (len %d) to peer %v", n, p.raddr)

	for pkt := range p.recv {
		switch pkt := pkt.(type) {
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
			p.tr.RemoteConnID = pkt.ConnectionID

			// Formulate a response.
			var orsp *aurp.OpenRspPacket
			switch {
			case pkt.Version != 1:
				// Respond with Open-Rsp with unknown version error.
				orsp = p.tr.NewOpenRspPacket(0, aurp.ErrCodeInvalidVersion, nil)

			case len(pkt.Options) > 0:
				// Options? OPTIONS? We don't accept no stinkin' _options_
				orsp = p.tr.NewOpenRspPacket(0, aurp.ErrCodeOptionNegotiation, nil)

			default:
				// Accept it I guess.
				orsp = p.tr.NewOpenRspPacket(0, 1, nil)
			}

			log.Printf("Responding with %T", orsp)

			if _, err := p.send(orsp); err != nil {
				log.Printf("Couldn't send Open-Rsp: %v", err)
			}

		case *aurp.OpenRspPacket:
			if pkt.RateOrErrCode < 0 {
				// It's an error code.
				log.Printf("Open-Rsp error code from peer %v: %d", p.raddr.IP, pkt.RateOrErrCode)
				// Close the connection
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

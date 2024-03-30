package main

import (
	"bytes"
	"context"
	"log"
	"net"
	"time"

	"gitea.drjosh.dev/josh/jrouter/aurp"
)

type peer struct {
	tr    *aurp.Transport
	conn  *net.UDPConn
	raddr *net.UDPAddr
	recv  chan aurp.Packet

	lastHeardFrom time.Time
}

// send encodes and sends pkt to the remote host.
func (p *peer) send(pkt aurp.Packet) (int, error) {
	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		return 0, err
	}
	return p.conn.WriteToUDP(b.Bytes(), p.raddr)
}

func (p *peer) handle(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	p.lastHeardFrom = time.Now()

	// Write an Open-Req packet
	n, err := p.send(p.tr.NewOpenReqPacket(nil))
	if err != nil {
		log.Printf("Couldn't send Open-Req packet: %v", err)
		return err
	}
	log.Printf("Sent Open-Req (len %d) to peer %v", n, p.raddr)

	for {
		select {
		case <-ctx.Done():
			// Send a best-effort Router Down before returning
			if _, err := p.send(p.tr.NewRDPacket(aurp.ErrCodeNormalClose)); err != nil {
				log.Printf("Couldn't send RD packet: %v", err)
			}
			return ctx.Err()

		case <-ticker.C:
			// TODO: time-based state changes
			// Check LHFT, send tickle?
			if time.Since(p.lastHeardFrom) > 10*time.Second {
				if _, err := p.send(p.tr.NewTicklePacket()); err != nil {
					log.Printf("Couldn't send Tickle: %v", err)
				}
			}

		case pkt := <-p.recv:
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
					orsp = p.tr.NewOpenRspPacket(0, int16(aurp.ErrCodeInvalidVersion), nil)

				case len(pkt.Options) > 0:
					// Options? OPTIONS? We don't accept no stinkin' _options_
					orsp = p.tr.NewOpenRspPacket(0, int16(aurp.ErrCodeOptionNegotiation), nil)

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

				// TODO: Make other requests

			case *aurp.RIReqPacket:
				// TODO: Respond with RI-Rsp

			case *aurp.RIRspPacket:
				// TODO: Repsond with RI-Ack
				// TODO: Integrate info into route table

			case *aurp.RIAckPacket:
				// TODO: Continue sending next RI-Rsp (streamed)
				// TODO: If SZI flag is set, send ZI-Rsp (transaction)

			case *aurp.RIUpdPacket:
				// TODO: Integrate info into route table

			case *aurp.RDPacket:
				// TODO: Remove router from tables
				// TODO: Close connection
				log.Printf("Router Down: error code %d %s", pkt.ErrorCode, pkt.ErrorCode)

			case *aurp.ZIReqPacket:
				// TODO: Respond with ZI-Rsp

			case *aurp.ZIRspPacket:
				// TODO: Integrate info into zone table

			case *aurp.TicklePacket:
				if _, err := p.send(p.tr.NewTickleAckPacket()); err != nil {
					log.Printf("Couldn't send Tickle-Ack: %v", err)
				}

			case *aurp.TickleAckPacket:
				p.lastHeardFrom = time.Now()
			}
		}
	}
}

package main

import (
	"context"
	"log"

	"gitea.drjosh.dev/josh/jrouter/atalk/nbp"
	"github.com/google/gopacket/pcap"
	"github.com/sfiera/multitalk/pkg/ddp"
)

type NBPMachine struct {
	aarp       *AARPMachine
	pcapHandle *pcap.Handle
}

func (NBPMachine) Run(ctx context.Context, incoming <-chan *ddp.ExtPacket) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ddpkt := <-incoming:
			if ddpkt.Proto != ddp.ProtoNBP {
				log.Printf("NBP: invalid DDP type %d on socket 2", ddpkt.Proto)
			}

			pkt, err := nbp.Unmarshal(ddpkt.Data)
			if err != nil {
				log.Printf("NBP: invalid packet: %v", err)
				continue
			}
			// TODO:
			log.Printf("NBP: Got %v", pkt.Function)
		}
	}
}

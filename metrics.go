package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	/*
		// jrouter_aurp_peers
		peersGauge = promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "jrouter",
				Subsystem: "aurp",
				Name:      "peers",
				Help:      "number of peers by connection state",
			},
			[]string{
				"sender_state",
				"reciever_state",
			},
		)
	*/

	// jrouter_aurp_bytes_in_total
	aurpBytesInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "aurp",
			Name:      "bytes_in_total",
			Help:      "count of AURP bytes received",
		},
		[]string{
			"peer",
		},
	)

	// jrouter_aurp_packets_in_total
	aurpPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "aurp",
			Name:      "packets_in_total",
			Help:      "count of AURP packets received",
		},
		[]string{
			"peer",
		},
	)

	// jrouter_aurp_invalid_packets_in_total
	aurpInvalidPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "aurp",
			Name:      "invalid_packets_in_total",
			Help:      "count of invalid AURP packets received",
		},
		[]string{
			"peer",
		},
	)
)

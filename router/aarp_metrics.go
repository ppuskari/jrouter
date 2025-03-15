package router

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// jrouter_aarp_bytes_in_total
	aarpBytesInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "aarp", Name: "bytes_in_total",
			Help: "count of AARP bytes received",
		},
		[]string{"port"},
	)

	// jrouter_aarp_packets_in_total
	aarpPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "aarp", Name: "packets_in_total",
			Help: "count of AARP packets received",
		},
		[]string{"port"},
	)

	// jrouter_aarp_bytes_out_total
	aarpBytesOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "aarp", Name: "bytes_out_total",
			Help: "count of AARP bytes sent",
		},
		[]string{"port"},
	)

	// jrouter_aarp_packets_out_total
	aarpPacketsOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "aarp", Name: "packets_out_total",
			Help: "count of AARP packets sent",
		},
		[]string{"port"},
	)
)

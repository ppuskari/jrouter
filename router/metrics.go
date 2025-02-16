package router

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (

	// jrouter_aarp_bytes_in_total
	aarpBytesInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "aarp",
			Name:      "bytes_in_total",
			Help:      "count of AARP bytes received",
		},
		[]string{
			"port",
		},
	)

	// jrouter_aarp_packets_in_total
	aarpPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "aarp",
			Name:      "packets_in_total",
			Help:      "count of AARP packets received",
		},
		[]string{
			"port",
		},
	)

	// jrouter_aarp_bytes_out_total
	aarpBytesOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "aarp",
			Name:      "bytes_out_total",
			Help:      "count of AARP bytes sent",
		},
		[]string{
			"port",
		},
	)

	// jrouter_aarp_packets_out_total
	aarpPacketsOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "aarp",
			Name:      "packets_out_total",
			Help:      "count of AARP packets sent",
		},
		[]string{
			"port",
		},
	)

	// jrouter_atalk_bytes_in_total
	atalkBytesInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "atalk",
			Name:      "bytes_in_total",
			Help:      "count of AppleTalk (DDP) bytes received",
		},
		[]string{
			"port",
			"src_net",
			"src_node",
			"src_socket",
			"dst_net",
			"dst_node",
			"dst_socket",
			"proto",
		},
	)

	// jrouter_atalk_packets_in_total
	atalkPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "atalk",
			Name:      "packets_in_total",
			Help:      "count of AppleTalk (DDP) packets received",
		},
		[]string{
			"port",
			"src_net",
			"src_node",
			"src_socket",
			"dst_net",
			"dst_node",
			"dst_socket",
			"proto",
		},
	)

	// jrouter_atalk_invalid_packets_in_total
	atalkInvalidPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "atalk",
			Name:      "invalid_packets_in_total",
			Help:      "count of invalid AARP or AppleTalk packets received",
		},
		[]string{
			"port",
		},
	)

	// jrouter_atalk_bytes_out_total
	atalkBytesOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "atalk",
			Name:      "bytes_out_total",
			Help:      "count of AppleTalk bytes sent",
		},
		[]string{
			"port",
			"src_net",
			"src_node",
			"src_socket",
			"dst_net",
			"dst_node",
			"dst_socket",
			"proto",
		},
	)

	// jrouter_atalk_packets_out_total
	atalkPacketsOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "atalk",
			Name:      "packets_out_total",
			Help:      "count of AppleTalk packets sent",
		},
		[]string{
			"port",
			"src_net",
			"src_node",
			"src_socket",
			"dst_net",
			"dst_node",
			"dst_socket",
			"proto",
		},
	)

	// jrouter_aurp_bytes_out_total
	aurpBytesOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "aurp",
			Name:      "bytes_out_total",
			Help:      "count of AURP bytes sent",
		},
		[]string{
			"peer",
		},
	)

	// jrouter_aurp_packets_out_total
	aurpPacketsOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "jrouter",
			Subsystem: "aurp",
			Name:      "packets_out_total",
			Help:      "count of AURP packets sent",
		},
		[]string{
			"peer",
		},
	)
)

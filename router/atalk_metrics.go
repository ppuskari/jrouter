/*
   Copyright 2025 Josh Deprez

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package router

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// jrouter_atalk_bytes_in_total
	atalkBytesInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "atalk", Name: "bytes_in_total",
			Help: "count of AppleTalk (DDP) bytes received",
		},
		[]string{"port", "src_net", "src_node", "src_socket", "dst_net", "dst_node", "dst_socket", "proto"},
	)

	// jrouter_atalk_packets_in_total
	atalkPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "atalk", Name: "packets_in_total",
			Help: "count of AppleTalk (DDP) packets received",
		},
		[]string{"port", "src_net", "src_node", "src_socket", "dst_net", "dst_node", "dst_socket", "proto"},
	)

	// jrouter_atalk_invalid_packets_in_total
	atalkInvalidPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "atalk", Name: "invalid_packets_in_total",
			Help: "count of invalid AARP or AppleTalk packets received",
		},
		[]string{"port"},
	)

	// jrouter_atalk_bytes_out_total
	atalkBytesOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "atalk", Name: "bytes_out_total",
			Help: "count of AppleTalk bytes sent",
		},
		[]string{"port", "src_net", "src_node", "src_socket", "dst_net", "dst_node", "dst_socket", "proto"},
	)

	// jrouter_atalk_packets_out_total
	atalkPacketsOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "atalk", Name: "packets_out_total",
			Help: "count of AppleTalk packets sent",
		},
		[]string{"port", "src_net", "src_node", "src_socket", "dst_net", "dst_node", "dst_socket", "proto"},
	)
)

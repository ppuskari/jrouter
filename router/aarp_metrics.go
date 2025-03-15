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

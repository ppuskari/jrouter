package router

import (
	"testing"

	"github.com/sfiera/multitalk/pkg/ddp"
)

func TestEtherTalkBroadcastNode(t *testing.T) {
	tests := []struct {
		name string
		node ddp.Node
		want bool
	}{
		{name: "any-router-node-zero", node: 0, want: true},
		{name: "network-broadcast-ff", node: 0xFF, want: true},
		{name: "ordinary-unicast-one", node: 1, want: false},
		{name: "ordinary-unicast-188", node: 188, want: false},
		{name: "reserved-fe-not-broadcast", node: 0xFE, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := etherTalkBroadcastNode(tc.node); got != tc.want {
				t.Fatalf("etherTalkBroadcastNode(%d) = %v, want %v", tc.node, got, tc.want)
			}
		})
	}
}

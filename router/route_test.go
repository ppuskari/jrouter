package router

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/sfiera/multitalk/pkg/ddp"
)

type fakeObserver struct {
	added, deleted, distChanged, routeChanged []*Route
}

func (o *fakeObserver) NetworkAdded(r *Route)           { o.added = append(o.added, r) }
func (o *fakeObserver) NetworkDeleted(r *Route)         { o.deleted = append(o.deleted, r) }
func (o *fakeObserver) NetworkDistanceChanged(r *Route) { o.distChanged = append(o.distChanged, r) }
func (o *fakeObserver) NetworkRouteChanged(r *Route)    { o.routeChanged = append(o.routeChanged, r) }

type fakeTarget struct {
	key   string
	class TargetClass
}

func (t fakeTarget) Forward(context.Context, *ddp.ExtPacket) error { return nil }
func (t fakeTarget) RouteTargetKey() string                        { return t.key }
func (t fakeTarget) Class() TargetClass                            { return t.class }

func TestRouteTable_Addition(t *testing.T) {
	rt := NewRouteTable()
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	wantObservers := map[RouteTableObserver]struct{}{
		obs: {},
	}
	observerComparable := cmpopts.EquateComparable(&fakeObserver{})
	if diff := cmp.Diff(rt.observers, wantObservers, observerComparable); diff != "" {
		t.Errorf("rt.observers diff (-got +want):\n%s", diff)
	}

	direct := fakeTarget{key: "direct", class: TargetClassDirect}
	peer := fakeTarget{key: "peer", class: TargetClassAURPPeer}

	directRoute, err := rt.UpsertRoute(direct, true, 100, 101, 0)
	if err != nil {
		t.Errorf("rt.UpsertRoute(direct, true, 100, 101, 0) error = %v", err)
	}
	peerRoute, err := rt.UpsertRoute(peer, true, 200, 201, 1)
	if err != nil {
		t.Errorf("rt.UpsertRoute(peer, true, 200, 201, 1) error = %v", err)
	}

	wantByClass := [TargetClassCount]map[RouteKey]*Route{
		TargetClassDirect:        {directRoute.RouteKey: directRoute},
		TargetClassAURPPeer:      {peerRoute.RouteKey: peerRoute},
		TargetClassAppleTalkPeer: {},
	}

	ignoreTimes := cmp.FilterValues(func(time.Time, time.Time) bool { return true }, cmp.Ignore())
	comparableTarget := cmpopts.EquateComparable(fakeTarget{})
	if diff := cmp.Diff(rt.byClass, wantByClass, ignoreTimes, comparableTarget); diff != "" {
		t.Errorf("rt.byClass diff (-got +want):\n%s", diff)
	}

	wantByNetwork := [1 << 16][]*Route{
		100: {directRoute},
		101: {directRoute},
		200: {peerRoute},
		201: {peerRoute},
	}

	if diff := cmp.Diff(rt.byNetwork, wantByNetwork, ignoreTimes, comparableTarget); diff != "" {
		t.Errorf("rt.byNetwork diff (-got +want):\n%s", diff)
	}

	// The observer should have not been informed of the new routes, because
	// they are invalid without zones.
	var wantObsAdded []*Route
	if diff := cmp.Diff(obs.added, wantObsAdded, ignoreTimes, comparableTarget); diff != "" {
		t.Errorf("obs.added diff (-got +want):\n%s", diff)
	}

	// Now add some zones.
	if err := rt.AddZonesToRoute(direct, 100, "The Twilight Zone"); err != nil {
		t.Errorf("rt.AddZonesToRoute(direct, 100, \"The Twilight Zone\") = %v", err)
	}
	if err := rt.AddZonesToRoute(peer, 200, "The Fright Zone"); err != nil {
		t.Errorf("rt.AddZonesToRoute(peer, 200, \"The Fright Zone\") = %v", err)
	}

	// Both routes should have been published.
	wantObsAdded = []*Route{directRoute, peerRoute}
	if diff := cmp.Diff(obs.added, wantObsAdded, ignoreTimes, comparableTarget); diff != "" {
		t.Errorf("obs.added diff (-got +want):\n%s", diff)
	}
}

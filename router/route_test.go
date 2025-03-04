package router

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/sfiera/multitalk/pkg/ddp"
)

// Helpful [cmp.Option]s
var (
	ignoreTimes        = cmp.FilterValues(func(time.Time, time.Time) bool { return true }, cmp.Ignore())
	comparableTarget   = cmpopts.EquateComparable(fakeTarget{})
	comparableObserver = cmpopts.EquateComparable(&fakeObserver{})
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

func TestRouteTable_AddObserver_RemoveObserver(t *testing.T) {
	rt := NewRouteTable()
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	wantObservers := map[RouteTableObserver]struct{}{
		obs: {},
	}
	if diff := cmp.Diff(rt.observers, wantObservers, comparableObserver); diff != "" {
		t.Errorf("rt.observers diff (-got +want):\n%s", diff)
	}

	rt.RemoveObserver(obs)
	wantObservers = map[RouteTableObserver]struct{}{}
	if diff := cmp.Diff(rt.observers, wantObservers, comparableObserver); diff != "" {
		t.Errorf("rt.observers diff (-got +want):\n%s", diff)
	}
}

func TestRouteTable_Upsert_Insertion(t *testing.T) {
	rt := NewRouteTable()
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	direct := fakeTarget{key: "direct", class: TargetClassDirect}
	aurpPeer := fakeTarget{key: "aurpPeer", class: TargetClassAURPPeer}
	etPeer := fakeTarget{key: "etPeer", class: TargetClassAppleTalkPeer}

	directRoute, err := rt.UpsertRoute(direct, true, 100, 101, 0)
	if err != nil {
		t.Errorf("rt.UpsertRoute(direct, true, 100, 101, 0) error = %v", err)
	}
	aurpRoute, err := rt.UpsertRoute(aurpPeer, true, 200, 201, 1)
	if err != nil {
		t.Errorf("rt.UpsertRoute(aurpPeer, true, 200, 201, 1) error = %v", err)
	}
	etRoute, err := rt.UpsertRoute(etPeer, true, 300, 301, 1)
	if err != nil {
		t.Errorf("rt.UpsertRoute(etPeer, true, 300, 301, 1) error = %v", err)
	}

	wantByClass := [TargetClassCount]map[RouteKey]*Route{
		TargetClassDirect:        {directRoute.RouteKey: directRoute},
		TargetClassAURPPeer:      {aurpRoute.RouteKey: aurpRoute},
		TargetClassAppleTalkPeer: {etRoute.RouteKey: etRoute},
	}

	if diff := cmp.Diff(rt.byClass, wantByClass, ignoreTimes, comparableTarget); diff != "" {
		t.Errorf("rt.byClass diff (-got +want):\n%s", diff)
	}

	wantByNetwork := [1 << 16][]*Route{
		100: {directRoute},
		101: {directRoute},
		200: {aurpRoute},
		201: {aurpRoute},
		300: {etRoute},
		301: {etRoute},
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
	if err := rt.AddZonesToRoute(aurpPeer, 200, "The Fright Zone"); err != nil {
		t.Errorf("rt.AddZonesToRoute(peer, 200, \"The Fright Zone\") = %v", err)
	}

	// Both routes should have been published.
	wantObsAdded = []*Route{directRoute, aurpRoute}
	if diff := cmp.Diff(obs.added, wantObsAdded, ignoreTimes, comparableTarget); diff != "" {
		t.Errorf("obs.added diff (-got +want):\n%s", diff)
	}
}

func TestRouteTable_Upsert_Updating(t *testing.T) {
	rt := NewRouteTable()
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	etPeer := fakeTarget{key: "etPeer", class: TargetClassAppleTalkPeer}
	if _, err := rt.UpsertRoute(etPeer, true, 300, 301, 1); err != nil {
		t.Errorf("rt.UpsertRoute(etPeer, true, 300, 301, 1) error = %v", err)
	}
	if err := rt.AddZonesToRoute(etPeer, 300, "TimeZone"); err != nil {
		t.Errorf("rt.AddZonesToRoute(etPeer, 300, \"TimeZone\") = %v", err)
	}

	// Now update it by re-upserting
	etRoute, err := rt.UpsertRoute(etPeer, true, 300, 301, 3)
	if err != nil {
		t.Errorf("rt.UpsertRoute(etPeer, true, 300, 301, 3) error = %v", err)
	}

	// Should still be only one route
	wantByClass := [TargetClassCount]map[RouteKey]*Route{
		TargetClassDirect:        {},
		TargetClassAURPPeer:      {},
		TargetClassAppleTalkPeer: {etRoute.RouteKey: etRoute},
	}
	if diff := cmp.Diff(rt.byClass, wantByClass, ignoreTimes, comparableTarget); diff != "" {
		t.Errorf("rt.byClass diff (-got +want):\n%s", diff)
	}
	wantByNetwork := [1 << 16][]*Route{
		300: {etRoute},
		301: {etRoute},
	}
	if diff := cmp.Diff(rt.byNetwork, wantByNetwork, ignoreTimes, comparableTarget); diff != "" {
		t.Errorf("rt.byNetwork diff (-got +want):\n%s", diff)
	}

	wantObsUpdated := []*Route{etRoute}
	if diff := cmp.Diff(obs.added, wantObsUpdated, ignoreTimes, comparableTarget); diff != "" {
		t.Errorf("obs.added diff (-got +want):\n%s", diff)
	}
}

func TestRouteTable_DeleteRoute(t *testing.T) {
	// TODO
}

func TestRouteTable_DeleteTarget(t *testing.T) {
	// TODO
}

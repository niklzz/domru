package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moleus/domru/pkg/sipclient"
)

func TestChooseIntercom(t *testing.T) {
	one := intercom{place: 1, control: 2, name: "door"}
	if pick, problem, _ := chooseIntercom([]intercom{one}); problem != "" || pick != one {
		t.Fatalf("single intercom must be picked: %+v %q", pick, problem)
	}
	if _, problem, retry := chooseIntercom(nil); problem == "" || !retry {
		t.Fatalf("no intercoms must keep retrying: %q %v", problem, retry)
	}
	if _, problem, retry := chooseIntercom([]intercom{one, {place: 1, control: 3}}); problem == "" || retry {
		t.Fatalf("several intercoms need explicit config, no retry: %q %v", problem, retry)
	}
}

func TestWatchSIPAlertsOnceAndOnRecovery(t *testing.T) {
	var state atomic.Value
	state.Store("ready")
	var mu sync.Mutex
	var sent []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchSIP(ctx, func() sipclient.Status { return sipclient.Status{State: state.Load().(string)} }, func(_ context.Context, text string) error {
		mu.Lock()
		sent = append(sent, text)
		mu.Unlock()
		return nil
	}, 5*time.Millisecond, 50*time.Millisecond)
	got := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), sent...) }
	state.Store("waiting_auth")
	time.Sleep(20 * time.Millisecond)
	state.Store("ready") // a short blip is not an outage
	time.Sleep(20 * time.Millisecond)
	if len(got()) != 0 {
		t.Fatalf("blip must stay silent: %q", got())
	}
	state.Store("waiting_auth")
	time.Sleep(150 * time.Millisecond)
	if s := got(); len(s) != 1 || !strings.Contains(s[0], "waiting_auth") {
		t.Fatalf("want one outage alert with the reason: %q", s)
	}
	state.Store("ready")
	time.Sleep(30 * time.Millisecond)
	if s := got(); len(s) != 2 || !strings.HasPrefix(s[1], "✅") {
		t.Fatalf("want a recovery message: %q", s)
	}
}

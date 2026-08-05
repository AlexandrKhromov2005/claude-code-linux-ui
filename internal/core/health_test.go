package core

import (
	"errors"
	"testing"
	"time"
)

// newTestMonitor builds a monitor with the network probes stubbed out so the
// state machine can be driven deterministically.
func newTestMonitor(window int) *HealthMonitor {
	m := &HealthMonitor{
		target:  "example:443",
		useTLS:  true,
		window:  window,
		timeout: time.Second,
		status:  ConnStatus{State: ConnChecking},
	}
	return m
}

func TestHealthVPNOffSkipsProbe(t *testing.T) {
	m := newTestMonitor(5)
	probed := false
	m.vpnFn = func() (string, bool) { return "", false }
	m.probeFn = func() (time.Duration, error) { probed = true; return 0, nil }

	m.check()

	if probed {
		t.Fatal("API must not be probed while the VPN is down")
	}
	if got := m.Status().State; got != ConnVPNOff {
		t.Fatalf("state = %q, want %q", got, ConnVPNOff)
	}
}

func TestHealthStableWhenReachable(t *testing.T) {
	m := newTestMonitor(5)
	m.vpnFn = func() (string, bool) { return "singbox_tun", true }
	m.probeFn = func() (time.Duration, error) { return 120 * time.Millisecond, nil }

	for range 5 {
		m.check()
	}
	st := m.Status()
	if st.State != ConnOK {
		t.Fatalf("state = %q, want %q", st.State, ConnOK)
	}
	if st.VPN != "singbox_tun" {
		t.Fatalf("vpn = %q, want singbox_tun", st.VPN)
	}
	if st.LatencyMs != 120 {
		t.Fatalf("latency = %d, want 120", st.LatencyMs)
	}
	if st.LossPct != 0 {
		t.Fatalf("loss = %d, want 0", st.LossPct)
	}
}

func TestHealthUnstableOnPartialLoss(t *testing.T) {
	m := newTestMonitor(5)
	m.vpnFn = func() (string, bool) { return "tun0", true }
	calls := 0
	// One failure in an otherwise-healthy window must read as unstable, not down.
	m.probeFn = func() (time.Duration, error) {
		calls++
		if calls == 3 {
			return 0, errors.New("timeout")
		}
		return 100 * time.Millisecond, nil
	}
	for range 5 {
		m.check()
	}
	st := m.Status()
	if st.State != ConnUnstable {
		t.Fatalf("state = %q, want %q", st.State, ConnUnstable)
	}
	if st.LossPct != 20 {
		t.Fatalf("loss = %d, want 20", st.LossPct)
	}
}

func TestHealthDownWhenAllFail(t *testing.T) {
	m := newTestMonitor(3)
	m.vpnFn = func() (string, bool) { return "tun0", true }
	m.probeFn = func() (time.Duration, error) { return 0, errors.New("no route") }

	for range 3 {
		m.check()
	}
	if got := m.Status().State; got != ConnDown {
		t.Fatalf("state = %q, want %q", got, ConnDown)
	}
}

func TestHealthUnstableOnHighLatency(t *testing.T) {
	m := newTestMonitor(3)
	m.vpnFn = func() (string, bool) { return "tun0", true }
	m.probeFn = func() (time.Duration, error) { return 2 * time.Second, nil }

	m.check()
	if got := m.Status().State; got != ConnUnstable {
		t.Fatalf("state = %q, want %q (high latency)", got, ConnUnstable)
	}
}

func TestHealthRecoversAfterVPNReturns(t *testing.T) {
	m := newTestMonitor(3)
	up := false
	m.vpnFn = func() (string, bool) { return "tun0", up }
	m.probeFn = func() (time.Duration, error) { return 90 * time.Millisecond, nil }

	up = false
	m.check() // vpn off, window cleared
	up = true
	m.check()
	m.check()
	m.check()
	if got := m.Status().State; got != ConnOK {
		t.Fatalf("state = %q, want %q after VPN returns", got, ConnOK)
	}
}

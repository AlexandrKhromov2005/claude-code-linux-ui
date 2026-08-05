package core

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ConnState classifies the live link that every turn depends on. The check is
// deliberately two-staged: first whether a VPN tunnel is actually up, and only
// then whether the Anthropic API is reachable through it. The API endpoint is
// only reachable via the tunnel here, so we never probe it while the tunnel is
// down — that would leak the attempt over the bare connection for nothing.
type ConnState string

const (
	ConnChecking ConnState = "checking" // no probe yet
	ConnVPNOff   ConnState = "vpn_off"  // no tunnel interface up — API not probed
	ConnDown     ConnState = "down"     // tunnel up but API unreachable
	ConnUnstable ConnState = "unstable" // tunnel up, API flaky (losses/latency)
	ConnOK       ConnState = "ok"       // tunnel up, API reachable and stable
)

// ConnStatus is a snapshot of connectivity for the UI.
type ConnStatus struct {
	State     ConnState `json:"state"`
	VPN       string    `json:"vpn"`       // detected tunnel interface, if any
	LatencyMs int       `json:"latencyMs"` // last successful handshake RTT
	LossPct   int       `json:"lossPct"`   // failed handshakes in the window, %
	Detail    string    `json:"detail"`    // human-readable explanation
	CheckedAt int64     `json:"checkedAt"` // unix seconds of the last check
}

// HealthMonitor periodically diagnoses the VPN tunnel and, when it is up, the
// stability of the path to the Anthropic API. It keeps a rolling window of
// handshake results so a single blip reads as "unstable", not "down".
type HealthMonitor struct {
	target   string        // host:port of the Anthropic API endpoint
	useTLS   bool          // handshake with TLS (https) vs a bare TCP connect
	interval time.Duration // gap between checks
	timeout  time.Duration // per-handshake deadline
	window   int           // rolling-window size for stability

	// Injectable for tests; default to the real network probes.
	probeFn func() (time.Duration, error)
	vpnFn   func() (string, bool)

	mu       sync.RWMutex
	results  []bool // handshake outcomes, newest last, capped at window
	lastLat  time.Duration
	status   ConnStatus
	onUpdate func(ConnStatus)
}

// NewHealthMonitor builds a monitor for the Anthropic API endpoint (honouring
// ANTHROPIC_BASE_URL when set, so a proxied base URL is probed instead).
func NewHealthMonitor() *HealthMonitor {
	target, useTLS := anthropicTarget()
	m := &HealthMonitor{
		target:   target,
		useTLS:   useTLS,
		interval: 8 * time.Second,
		timeout:  5 * time.Second,
		window:   5,
		status:   ConnStatus{State: ConnChecking, Detail: "проверка связи…"},
	}
	m.probeFn = m.handshake
	m.vpnFn = detectVPN
	return m
}

// Status returns the latest snapshot.
func (m *HealthMonitor) Status() ConnStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// SetOnUpdate registers a callback invoked after every check with the fresh
// status, so a transport can push it to connected clients.
func (m *HealthMonitor) SetOnUpdate(fn func(ConnStatus)) {
	m.mu.Lock()
	m.onUpdate = fn
	m.mu.Unlock()
}

// Run checks immediately and then on every interval tick until ctx is done.
func (m *HealthMonitor) Run(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.check()
			t.Reset(m.interval)
		}
	}
}

// check runs one full VPN-then-API diagnosis and publishes the result.
func (m *HealthMonitor) check() {
	iface, up := m.vpnFn()

	m.mu.Lock()
	if !up {
		// Tunnel is down: never probe the API. Drop the stale window so the next
		// time the VPN comes back the stability verdict is rebuilt from scratch.
		m.results = nil
		m.lastLat = 0
		m.status = ConnStatus{
			State:     ConnVPNOff,
			Detail:    "VPN не поднят — включите туннель (проверка API не выполняется)",
			CheckedAt: time.Now().Unix(),
		}
		st := m.status
		cb := m.onUpdate
		m.mu.Unlock()
		if cb != nil {
			cb(st)
		}
		return
	}
	m.mu.Unlock()

	lat, err := m.probeFn()

	m.mu.Lock()
	m.results = append(m.results, err == nil)
	if len(m.results) > m.window {
		m.results = m.results[len(m.results)-m.window:]
	}
	if err == nil {
		m.lastLat = lat
	}
	m.status = m.classifyLocked(iface)
	st := m.status
	cb := m.onUpdate
	m.mu.Unlock()
	if cb != nil {
		cb(st)
	}
}

// classifyLocked turns the rolling window into a state. Caller holds m.mu.
func (m *HealthMonitor) classifyLocked(iface string) ConnStatus {
	n := len(m.results)
	ok := 0
	for _, r := range m.results {
		if r {
			ok++
		}
	}
	fails := n - ok
	st := ConnStatus{
		VPN:       iface,
		LatencyMs: int(m.lastLat / time.Millisecond),
		CheckedAt: time.Now().Unix(),
	}
	if n > 0 {
		st.LossPct = fails * 100 / n
	}
	switch {
	case ok == 0:
		st.State = ConnDown
		st.LatencyMs = 0
		st.Detail = "VPN поднят, но Anthropic API недоступен"
	case fails > 0 || m.lastLat > 1500*time.Millisecond:
		st.State = ConnUnstable
		st.Detail = "связь нестабильна (потери или высокая задержка)"
	default:
		st.State = ConnOK
		st.Detail = "связь с Anthropic стабильна"
	}
	return st
}

// handshake opens (and immediately closes) a TCP/TLS connection to the API
// endpoint, timing the round trip. It sends no request and no credentials, so
// it consumes zero API tokens — it only proves the tunnelled path is alive.
func (m *HealthMonitor) handshake() (time.Duration, error) {
	start := time.Now()
	d := net.Dialer{Timeout: m.timeout}
	if !m.useTLS {
		conn, err := d.Dial("tcp", m.target)
		if err != nil {
			return 0, err
		}
		conn.Close()
		return time.Since(start), nil
	}
	host, _, err := net.SplitHostPort(m.target)
	if err != nil {
		host = m.target
	}
	conn, err := tls.DialWithDialer(&d, "tcp", m.target, &tls.Config{ServerName: host})
	if err != nil {
		return 0, err
	}
	conn.Close()
	return time.Since(start), nil
}

// anthropicTarget resolves the endpoint to probe. It defaults to the public API
// host and follows ANTHROPIC_BASE_URL when the CLI is pointed at a proxy.
func anthropicTarget() (host string, useTLS bool) {
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")); v != "" {
		if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
			useTLS = u.Scheme != "http"
			port := u.Port()
			if port == "" {
				if useTLS {
					port = "443"
				} else {
					port = "80"
				}
			}
			return net.JoinHostPort(u.Hostname(), port), useTLS
		}
	}
	return "api.anthropic.com:443", true
}

// detectVPN reports the name of an up tunnel interface, if one exists. It does
// not rely on the default route: split-tunnel setups (e.g. sing-box/WireGuard
// with policy routing) keep the default on the physical link and steer traffic
// via ip rules, so the presence of a live tunnel interface is the honest signal.
func detectVPN() (string, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", false
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if !looksLikeTunnel(ifi) {
			continue
		}
		addrs, _ := ifi.Addrs()
		if len(addrs) == 0 {
			continue // interface up but not carrying an address yet
		}
		return ifi.Name, true
	}
	return "", false
}

// tunnelHints matches interface names of common VPN/proxy clients.
var tunnelHints = []string{
	"tun", "tap", "wg", "ppp", "sing", "proton", "nord",
	"mullvad", "utun", "wgcf", "gpd", "tailscale", "tail", "vpn", "ipsec",
}

// looksLikeTunnel decides whether an interface is a VPN tunnel using several
// independent signals so no single client naming convention is load-bearing.
func looksLikeTunnel(ifi net.Interface) bool {
	// Bridges and virtual switches carry addresses but are not tunnels; exclude
	// the obvious local ones before the positive checks.
	name := strings.ToLower(ifi.Name)
	if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") ||
		strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "virbr") {
		return false
	}
	if ifi.Flags&net.FlagPointToPoint != 0 {
		return true
	}
	for _, h := range tunnelHints {
		if strings.Contains(name, h) {
			return true
		}
	}
	// TUN/TAP devices expose tun_flags; WireGuard reports DEVTYPE=wireguard.
	if _, err := os.Stat("/sys/class/net/" + ifi.Name + "/tun_flags"); err == nil {
		return true
	}
	if b, err := os.ReadFile("/sys/class/net/" + ifi.Name + "/uevent"); err == nil {
		if strings.Contains(string(b), "DEVTYPE=wireguard") {
			return true
		}
	}
	return false
}

package main

import "testing"

func TestParseServeArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantAddr   string
		wantRotate bool
	}{
		{"no arguments", nil, defaultServeAddr, false},
		{"address only", []string{"127.0.0.1:9000"}, "127.0.0.1:9000", false},
		{"flag only", []string{"--new-token"}, defaultServeAddr, true},
		// The flag must work on either side of the address, since nobody
		// remembers which order a hand-rolled parser wanted.
		{"flag after address", []string{"127.0.0.1:9000", "--new-token"}, "127.0.0.1:9000", true},
		{"flag before address", []string{"--new-token", "127.0.0.1:9000"}, "127.0.0.1:9000", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, rotate := parseServeArgs(tt.args)
			if addr != tt.wantAddr {
				t.Errorf("addr = %q, want %q", addr, tt.wantAddr)
			}
			if rotate != tt.wantRotate {
				t.Errorf("rotate = %v, want %v", rotate, tt.wantRotate)
			}
		})
	}
}

// The flag must never be mistaken for an address: binding to "--new-token"
// would fail with a confusing error instead of rotating.
func TestParseServeArgsFlagIsNotAnAddress(t *testing.T) {
	addr, _ := parseServeArgs([]string{"--new-token"})
	if addr == "--new-token" {
		t.Fatal("the flag was taken as the listen address")
	}
}

package main

import (
	"net"
	"testing"
)

// ipv4 builds a minimal IPv4 header with the given proto/src/dst for parse tests.
func ipv4(proto byte, src, dst string, extra int) []byte {
	b := make([]byte, 20+extra)
	b[0] = 0x45 // version 4, ihl 5
	b[9] = proto
	copy(b[12:16], net.ParseIP(src).To4())
	copy(b[16:20], net.ParseIP(dst).To4())
	return b
}

func TestParseIPv4(t *testing.T) {
	proto, src, dst, ok := parseIPv4(ipv4(6, "10.42.0.2", "142.250.1.2", 20))
	if !ok || proto != 6 || !src.Equal(net.ParseIP("10.42.0.2")) || !dst.Equal(net.ParseIP("142.250.1.2")) {
		t.Fatalf("parse: ok=%v proto=%d src=%v dst=%v", ok, proto, src, dst)
	}
	// Runt, IPv6 nibble, and empty must be rejected, not panic.
	for _, bad := range [][]byte{nil, {0x45, 0x00}, {0x60, 0, 0, 0}, make([]byte, 19)} {
		if _, _, _, ok := parseIPv4(bad); ok {
			t.Fatalf("expected reject for %v", bad)
		}
	}
	// A parsed src must not alias later reads of the same buffer.
	buf := ipv4(17, "1.1.1.1", "8.8.8.8", 0)
	_, src2, _, _ := parseIPv4(buf)
	copy(buf[12:16], net.ParseIP("2.2.2.2").To4())
	if !src2.Equal(net.ParseIP("1.1.1.1")) {
		t.Fatalf("parseIPv4 aliased the read buffer: got %v", src2)
	}
}

func TestTrackable(t *testing.T) {
	yes := []string{"142.250.1.2", "1.1.1.1", "8.8.8.8", "203.0.113.9"}
	no := []string{
		"10.42.0.2", "10.0.0.5", "192.168.1.1", "172.16.9.9", "100.64.0.1",
		"127.0.0.1", "169.254.1.1", "224.0.0.1", "255.255.255.255", "0.0.0.0",
	}
	for _, s := range yes {
		if !trackable(net.ParseIP(s)) {
			t.Errorf("%s should be trackable", s)
		}
	}
	for _, s := range no {
		if trackable(net.ParseIP(s)) {
			t.Errorf("%s should NOT be trackable", s)
		}
	}
	if trackable(net.ParseIP("2606:4700::1111")) {
		t.Errorf("IPv6 not handled yet; should be untracked")
	}
}

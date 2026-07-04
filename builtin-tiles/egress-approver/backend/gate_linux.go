//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// The dataplane. buxond hands us one point-to-point link per bound client
// (bxc0, bxc1, …, each a /30 in 10.42.0.0/16) and our own egress (bx0). We turn
// this netns into a router and gate *forwarded* client traffic per destination.
//
// Enforcement is policy routing, not a firewall binary: a single `ip rule`
// diverts anything sourced from the client link space into a dedicated table
// whose default is a blackhole, so unapproved client traffic is dropped —
// while our own traffic (sourced from bx0) keeps using the main table and
// reaches the internet for reverse-DNS/RDAP. Approving an IP adds a /32 route
// for it into that table; revoking removes it. iproute2 does the same netlink
// ops the base rootfs uses to configure interfaces, so it works in this
// rootless netns (it has CAP_NET_ADMIN over its own network namespace).

const (
	gateTable   = "100"          // custom routing table for gated client traffic
	clientCIDR  = "10.42.0.0/16" // the client /30 link space
	clientIface = "bxc"          // per-client link prefix
	egressIface = "bx0"          // our own egress
	ethPAll     = 0x0003         // ETH_P_ALL
	pktOutgoing = 4              // PACKET_OUTGOING (linux/if_packet.h)
)

// discoverLinks finds our egress and counts the client links buxond created.
func discoverLinks() (egress string, clients int) {
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		switch {
		case i.Name == egressIface:
			egress = egressIface
		case strings.HasPrefix(i.Name, clientIface):
			clients++
		}
	}
	return egress, clients
}

// setupGate enables forwarding and installs the default-deny policy route.
func setupGate(egress string) error {
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("ip_forward: %w", err)
	}
	// Divert client-sourced traffic into the gated table (idempotent).
	ipRun("rule", "add", "from", clientCIDR, "lookup", gateTable)
	// Default there is a blackhole: no approval, no egress.
	ipRun("route", "add", "blackhole", "default", "table", gateTable)
	return nil
}

// applyGate reconciles the gated table so exactly the approved IPs have a route
// out. We rebuild it wholesale (flush → blackhole default → one /32 per IP):
// simple and always consistent, and the set is human-sized.
func applyGate(egress string, approved []string) error {
	if _, err := exec.LookPath("ip"); err != nil {
		return nil // iproute2 not installed (e.g. dev host) — nothing to enforce
	}
	ipRun("route", "flush", "table", gateTable)
	ipRun("route", "add", "blackhole", "default", "table", gateTable)
	if egress == "" {
		return nil // no egress bound yet — approved traffic has nowhere to go
	}
	for _, ip := range approved {
		ipRun("route", "add", ip+"/32", "dev", egress, "table", gateTable)
	}
	return nil
}

// ipRun runs one iproute2 command, tolerating "already exists" on adds.
func ipRun(args ...string) {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "File exists") {
		logf("ip %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

// sniffClients taps every client link with an AF_PACKET socket and reports each
// packet — including ones the gate drops — so we can surface new destinations
// and meter bytes. On a TUN there is no link header, so the payload is raw IPv4.
func sniffClients(onFlow func(remote net.IP, inbound bool, n int)) {
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if strings.HasPrefix(i.Name, clientIface) {
			go sniff(i.Index, onFlow)
		}
	}
}

func sniff(ifindex int, onFlow func(remote net.IP, inbound bool, n int)) {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_DGRAM, int(htons(ethPAll)))
	if err != nil {
		logf("af_packet socket: %v", err)
		return
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{
		Protocol: htons(ethPAll), Ifindex: ifindex,
	}); err != nil {
		logf("af_packet bind: %v", err)
		return
	}
	buf := make([]byte, 65536)
	for {
		n, from, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return
		}
		_, src, dst, ok := parseIPv4(buf[:n])
		if !ok {
			continue
		}
		// PACKET_OUTGOING = kernel sending toward the client = a return from the
		// internet (remote is the source); otherwise it came from the client and
		// is outbound (remote is the destination).
		inbound := false
		if ll, ok := from.(*syscall.SockaddrLinklayer); ok && ll.Pkttype == pktOutgoing {
			inbound = true
		}
		if inbound {
			onFlow(src, true, n)
		} else {
			onFlow(dst, false, n)
		}
	}
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

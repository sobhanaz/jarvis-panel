package main

// Link probe: is the GRE peer reachable, and how fast?
//
// ICMP is tried first, but many ISPs drop ICMP inside GRE while TCP flows
// normally, so an unanswered ping is not proof of a dead tunnel (and must not
// make the whole dashboard claim the tunnel is down). The fallbacks measure the
// path with real TCP: the kernel's own RTT of an established session through
// the tunnel, then a TCP connect to the peer's FRP port.

import (
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type linkProbe struct {
	OK  bool
	Ms  string // e.g. "223ms"
	Via string // "icmp" | "tcp"
}

// Package-level hooks so tests never touch the network or spawn processes.
var (
	pingFn = func(ip string) (time.Duration, bool) {
		start := time.Now()
		if err := exec.Command("ping", "-c", "1", "-W", "2", ip).Run(); err != nil {
			return 0, false
		}
		return time.Since(start), true
	}
	sessionsFn = func() string {
		out, _ := exec.Command("ss", "-Htin", "state", "established").Output()
		return string(out)
	}
	dialFn = func(addr string) (time.Duration, bool) {
		start := time.Now()
		c, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			return 0, false
		}
		_ = c.Close()
		return time.Since(start), true
	}
)

func fmtMs(d time.Duration) string {
	return fmt.Sprintf("%.0fms", float64(d.Microseconds())/1000)
}

// probeLink checks reachability of peerIP. tcpPort > 0 enables the connect
// fallback (only meaningful on the client side, where the peer listens).
func probeLink(peerIP string, tcpPort int) linkProbe {
	if peerIP == "" {
		return linkProbe{}
	}
	if d, ok := pingFn(peerIP); ok {
		return linkProbe{OK: true, Ms: fmtMs(d), Via: "icmp"}
	}
	if ms, ok := parseSSRTT(sessionsFn(), peerIP); ok {
		return linkProbe{OK: true, Ms: fmt.Sprintf("%.0fms", ms), Via: "tcp"}
	}
	if tcpPort > 0 {
		if d, ok := dialFn(net.JoinHostPort(peerIP, strconv.Itoa(tcpPort))); ok {
			return linkProbe{OK: true, Ms: fmtMs(d), Via: "tcp"}
		}
	}
	return linkProbe{}
}

var (
	ssConnLine = regexp.MustCompile(`^\s*\d+\s+\d+\s+(\S+)\s+(\S+)`)
	ssRTT      = regexp.MustCompile(`\brtt:([0-9.]+)/`)
)

// hostOf strips the port, brackets and the ::ffff: IPv4-mapped prefix from an
// `ss` address such as "[::ffff:10.10.10.2]:39015" or "10.10.10.1:37096".
func hostOf(addr string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr
	}
	h := strings.Trim(addr[:i], "[]")
	return strings.TrimPrefix(h, "::ffff:")
}

// parseSSRTT returns the lowest kernel-measured RTT (ms) among established TCP
// sessions that involve peerIP, from `ss -Htin state established` output.
func parseSSRTT(out, peerIP string) (float64, bool) {
	best, found := 0.0, false
	match := false
	for _, line := range strings.Split(out, "\n") {
		if m := ssConnLine.FindStringSubmatch(line); m != nil {
			match = hostOf(m[1]) == peerIP || hostOf(m[2]) == peerIP
			continue
		}
		if !match {
			continue
		}
		if m := ssRTT.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 0 && (!found || v < best) {
				best, found = v, true
			}
		}
		match = false
	}
	return best, found
}

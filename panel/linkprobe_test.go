package main

import (
	"testing"
	"time"
)

// Real `ss -Htin state established` shape: a connection line, then an indented info line.
const ssFixture = `0      0                  10.10.10.1:37096            10.10.10.2:39015
	 cubic wscale:13,13 rto:423 rtt:222.413/0.213 mss:1328 unacked:2 lastack:214
0      0       [::ffff:10.12.0.2]:7001      [::ffff:10.12.0.1]:53032
	 cubic wscale:13,13 rto:300 rtt:41.5/3.1 mss:1328
0      0                  10.0.0.1:55            110.10.10.2:80
	 cubic rto:200 rtt:1.0/0.5
0      0                  10.10.10.1:40000            10.10.10.2:39015
	 cubic rto:230 rtt:150.2/1.0
`

func TestParseSSRTT(t *testing.T) {
	cases := []struct {
		name   string
		peer   string
		want   float64
		wantOK bool
	}{
		{"lowest of two sessions; the 110.10.10.2 decoy (rtt 1.0) is NOT matched", "10.10.10.2", 150.2, true},
		{"IPv4-mapped address", "10.12.0.1", 41.5, true},
		{"IPv4-mapped, we are the local side", "10.12.0.2", 41.5, true},
		{"match on the local side of a line (decoy 110.10.10.2 on the far side)", "10.0.0.1", 1.0, true},
		{"unknown peer", "10.99.0.9", 0, false},
	}
	for _, c := range cases {
		got, ok := parseSSRTT(ssFixture, c.peer)
		if ok != c.wantOK || got != c.want {
			t.Errorf("%s: parseSSRTT(%q) = (%v, %v), want (%v, %v)", c.name, c.peer, got, ok, c.want, c.wantOK)
		}
	}
	if _, ok := parseSSRTT("", "10.10.10.2"); ok {
		t.Error("empty ss output must not match")
	}
	// a session line with no info line must not leak into the next connection
	got, ok := parseSSRTT("0 0 10.10.10.1:1 10.10.10.2:2\n0 0 10.7.7.1:1 10.7.7.2:2\n\t rtt:9.0/1.0\n", "10.10.10.2")
	if ok {
		t.Errorf("rtt of the next connection was attributed to the wrong peer: %v", got)
	}
}

func TestProbeLinkOrderAndFallbacks(t *testing.T) {
	origPing, origSS, origDial := pingFn, sessionsFn, dialFn
	defer func() { pingFn, sessionsFn, dialFn = origPing, origSS, origDial }()

	dialed := 0
	pingFn = func(string) (time.Duration, bool) { return 12 * time.Millisecond, true }
	sessionsFn = func() string { return ssFixture }
	dialFn = func(string) (time.Duration, bool) { dialed++; return 5 * time.Millisecond, true }

	if p := probeLink("10.10.10.2", 39015); !p.OK || p.Via != "icmp" || p.Ms != "12ms" {
		t.Errorf("ICMP answers: got %+v", p)
	}

	// The reported incident: ICMP filtered inside GRE, but the TCP session is healthy.
	pingFn = func(string) (time.Duration, bool) { return 0, false }
	p := probeLink("10.10.10.2", 39015)
	if !p.OK || p.Via != "tcp" || p.Ms != "150ms" {
		t.Errorf("ICMP filtered + live session must be reachable via tcp with its RTT, got %+v", p)
	}
	if dialed != 0 {
		t.Error("must not open a new connection when a live session already proves the path")
	}

	// No session yet (client just restarted): connect to the peer's FRP port.
	sessionsFn = func() string { return "" }
	if p := probeLink("10.10.10.2", 39015); !p.OK || p.Via != "tcp" || p.Ms != "5ms" || dialed != 1 {
		t.Errorf("connect fallback: got %+v (dialed=%d)", p, dialed)
	}
	// Server side has no listening port on the peer to dial.
	if p := probeLink("10.10.10.2", 0); p.OK {
		t.Errorf("no signal at all must be reported as unreachable, got %+v", p)
	}
	dialFn = func(string) (time.Duration, bool) { return 0, false }
	if p := probeLink("10.10.10.2", 39015); p.OK {
		t.Errorf("dead peer reported reachable: %+v", p)
	}
	if p := probeLink("", 39015); p.OK {
		t.Errorf("empty peer address reported reachable: %+v", p)
	}
}

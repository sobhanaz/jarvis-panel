#!/usr/bin/env bash
# Tests for the watchdog engine in hashem.sh.
# Every external signal (ip, ss, ping, systemctl, clock) is stubbed, so this runs
# anywhere without root, without GRE, without touching the network.
T_NAME="watchdog"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/lib.sh
source "$HERE/lib.sh"
SCRIPT="${HASHEM_SCRIPT_UNDER_TEST:-$HERE/../hashem.sh}"
# shellcheck source=/dev/null
source "$SCRIPT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
WD_STATE_DIR="$TMP/state"
WATCHDOG_FILE="$TMP/watchdog.json"
PEERS_FILE="$TMP/peers.json"

assert_eq "script is source-safe (no dispatcher ran)" "1" "$HASHEM_SOURCED"

# ------------------------------------------------------------------------------
section "peer GRE address (regression: 'local IFS=. read -r' was a syntax error)"
ip() {
    case "$*" in
        "link show gre-tunnel") return 0 ;;
        "-4 addr show dev gre-tunnel") echo "    inet ${FAKE_INNER}/30 scope global gre-tunnel" ;;
    esac
}
for pair in "10.10.10.2 10.10.10.1" "10.10.10.1 10.10.10.2" "10.12.0.2 10.12.0.1" \
            "10.12.0.1 10.12.0.2" "172.16.5.6 172.16.5.5" "172.16.5.5 172.16.5.6"; do
    set -- $pair
    FAKE_INNER="$1"
    assert_eq "inner $1 -> peer $2" "$2" "$(watchdog_get_peer_gre 2>&1)"
done
unset -f ip

# ------------------------------------------------------------------------------
section "signals: established session / black-hole detection (fixture = real 'ss' output shape)"
SS_FIXTURE_TN='0      0                  10.10.10.1:37096            10.10.10.2:39015
0      0       [::ffff:10.10.10.2]:39015     [::ffff:10.10.10.1]:53032'
SS_STUCK='0      175809             10.10.10.1:37096             10.10.10.2:39015
	 cubic wscale:13,13 rto:423 rtt:222.413/0.213 mss:1328 bytes_sent:2000 bytes_acked:900 unacked:12 retrans:0/5248 lastsnd:100 lastrcv:5 lastack:45000
0      0                  10.99.0.1:1111              10.99.0.2:2222
	 cubic wscale:13,13 rto:200 unacked:0 lastack:100'
SS_BUSY_OK='0      4000               10.10.10.1:37096             10.10.10.2:39015
	 cubic wscale:13,13 rto:423 unacked:5 retrans:0/3 lastsnd:100 lastrcv:5 lastack:150'
SS_IDLE='0      0                  10.10.10.1:37096             10.10.10.2:39015
	 cubic wscale:13,13 rto:423 unacked:0 lastsnd:100 lastrcv:5 lastack:900000'
SS_DECOY='0      0                  10.0.0.1:37096             110.10.10.2:39015'
ss() { # $* contains the flags; -t -i prints the info lines
    case "$*" in
        *-Htin*) printf '%s\n' "$SS_OUT" ;;
        *-Htn*) printf '%s\n' "$SS_OUT" | grep -vE '^[[:space:]]' ;;
    esac
}
SS_OUT="$SS_FIXTURE_TN"
wd_sig_session 10.10.10.2 && ok "plain peer address matches" || bad "plain peer address matches"
SS_OUT='0      0   [::ffff:10.10.10.2]:39015     [::ffff:10.10.10.1]:53032'
wd_sig_session 10.10.10.2 && ok "IPv4-mapped (::ffff:) peer matches" || bad "IPv4-mapped peer matches"
SS_OUT="$SS_DECOY"
wd_sig_session 10.10.10.2 && bad "110.10.10.2 must NOT match 10.10.10.2" || ok "110.10.10.2 does not match 10.10.10.2"
SS_OUT="$SS_STUCK"
wd_sig_stuck 10.10.10.2 && ok "un-ACKed data + no ACK for 45s = stuck" || bad "stuck session not detected"
wd_sig_stuck 10.99.0.2  && bad "healthy other session reported stuck" || ok "other peer's session is not stuck"
SS_OUT="$SS_BUSY_OK"
wd_sig_stuck 10.10.10.2 && bad "busy but ACKing session reported stuck" || ok "busy session with fresh ACKs is not stuck"
SS_OUT="$SS_IDLE"
wd_sig_stuck 10.10.10.2 && bad "idle session (nothing unacked) reported stuck" || ok "idle session is not stuck"
unset -f ss

# ------------------------------------------------------------------------------
section "wd_fault: what is wrong, from the signals"
#                            role   gre_up frp       link session stuck
assert_eq "client healthy"              ""           "$(wd_fault client 1 active     1 1 0)"
assert_eq "ICMP blocked but session ok" ""           "$(wd_fault client 1 active     1 1 0)"
assert_eq "client, GRE interface down"  "gre-down"   "$(wd_fault client 0 active     0 0 0)"
assert_eq "client, nothing reachable"   "link-down"  "$(wd_fault client 1 activating 0 0 0)"
assert_eq "client, reachable, no login" "no-session" "$(wd_fault client 1 inactive   1 0 0)"
assert_eq "client, black-holed session" "stuck"      "$(wd_fault client 1 active     1 1 1)"
assert_eq "server healthy"              ""           "$(wd_fault server 1 active     1 1 0)"
assert_eq "server healthy, no clients"  ""           "$(wd_fault server 1 active     1 0 0)"
assert_eq "server, frps stopped"        "frp-down"   "$(wd_fault server 1 inactive   1 1 0)"
assert_eq "server, peer silent"         "waiting"    "$(wd_fault server 1 active     0 0 0)"
assert_eq "server, black-holed session" "stuck"      "$(wd_fault server 1 active     1 1 1)"

# ------------------------------------------------------------------------------
section "wd_decide: threshold, backoff, escalation"
assert_eq "healthy -> nothing"                 "up NONE ok"                       "$(wd_decide '' 0 0 999999 2)"
assert_eq "peer silent never triggers a restart" "waiting NONE peer-silent"        "$(wd_decide waiting 9 0 999999 2)"
assert_eq "1st bad tick is only counted"       "down NONE link-down(1/2)"         "$(wd_decide link-down 1 0 999999 2)"
assert_eq "threshold reached -> repair"        "down RESTART_BOTH link-down"      "$(wd_decide link-down 2 0 999999 2)"
assert_eq "vanished GRE interface: immediate"  "down RESTART_GRE gre-down"        "$(wd_decide gre-down 1 0 999999 2)"
assert_eq "frps down -> restart only frps"     "down RESTART_FRP frp-down"        "$(wd_decide frp-down 2 0 999999 2)"
assert_eq "stuck: first try is the cheap one"  "down RESTART_FRP stuck"          "$(wd_decide stuck 2 0 999999 2)"
assert_eq "stuck again: escalate to both"      "down RESTART_BOTH stuck"          "$(wd_decide stuck 3 1 999999 2)"
assert_eq "no-session: first try is frpc only" "down RESTART_FRP no-session"      "$(wd_decide no-session 2 0 999999 2)"
assert_eq "link-down, 1st repair is not 'persistent'" "down RESTART_BOTH link-down"             "$(wd_decide link-down 9 1 999999 2)"
assert_eq "link-down after 2 failed repairs: hint"    "down RESTART_BOTH link-down+persistent"  "$(wd_decide link-down 9 2 999999 2)"
assert_eq "stuck after 2 failed repairs: hint"        "down RESTART_BOTH stuck+persistent"      "$(wd_decide stuck 9 2 999999 2)"
assert_eq "frp-down is never 'persistent'"            "down RESTART_FRP frp-down"               "$(wd_decide frp-down 9 4 999999 2)"
assert_eq "the watchdog never switches carrier by itself" "0" "$(wd_decide link-down 9 6 999999 2 | grep -c SWITCH)"
assert_contains "cooldown after 1st repair = 240s"  "$(wd_decide link-down 9 1 0 2)"   "next attempt in 240s"
assert_contains "cooldown after 2nd repair = 480s"  "$(wd_decide link-down 9 2 0 2)"   "next attempt in 480s"
assert_contains "cooldown after 3rd repair = 960s"  "$(wd_decide link-down 9 3 0 2)"   "next attempt in 960s"
assert_contains "cooldown is capped at 1800s"       "$(wd_decide link-down 9 8 0 2)"   "next attempt in 1800s"
assert_eq "repair allowed once the cooldown ended"  "down RESTART_BOTH link-down" "$(wd_decide link-down 9 1 240 2)"

# ------------------------------------------------------------------------------
section "simulation: engine with stubbed signals and a fake clock"
LOG="$TMP/actions.log"
CLOCK=1000000
wd_now() { echo "$CLOCK"; }
sleep() { :; }
systemctl() { echo "$CLOCK systemctl $*" >> "$LOG"; }
carrier_cycle_next() { echo "$CLOCK carrier_cycle_next" >> "$LOG"; echo "wss:8443"; }
wd_cfg_get() { case "$1" in fail_threshold) echo 2 ;; *) echo "$2" ;; esac; }
TUNNELS="main|client|gre-tunnel|10.10.10.1|frpc|39015|main"
wd_list_tunnels() { printf '%s\n' "$TUNNELS"; }
PROBE="1 active 1 1 0 session"
wd_probe() { # per-tunnel override via PROBE_<id>, default PROBE
    local id="${1%%|*}" var="PROBE_${1%%|*}"
    echo "${!var:-$PROBE}"
}

reset_sim() { rm -rf "$WD_STATE_DIR" "$LOG"; : > "$LOG"; CLOCK=1000000; }
run_ticks() { # n  -- one tick per simulated minute
    local i
    for (( i = 0; i < $1; i++ )); do wd_run_tick > "$TMP/last_tick.txt"; CLOCK=$(( CLOCK + 60 )); done
}
restart_calls() { grep -c "systemctl restart" "$LOG"; }

# The reported incident: ICMP is filtered but the FRP session is healthy.
reset_sim; PROBE="1 active 1 1 0 session"
run_ticks 60
assert_eq "ICMP filtered + healthy session: zero restarts in an hour" "0" "$(restart_calls)"
assert_contains "...and the status the panel reads is up" "$(wd_run_tick)" "status=up"

# Persistent real outage: the old code restarted every tunnel every ~2 minutes.
reset_sim; PROBE="1 activating 0 0 0 none"
run_ticks 60
n=$(restart_calls)
assert_ge "persistent outage is still repaired at least once" "$n" 2
assert_le "...but at most a handful of attempts per hour (old code: ~58)" "$n" 12
times=$(grep "systemctl restart gre-tunnel.service" "$LOG" | awk '{print $1}' | tr '\n' ' ')
prev=0; prevgap=0; monotone=1
for t in $times; do
    if (( prev > 0 )); then gap=$(( t - prev )); (( gap < prevgap )) && monotone=0; prevgap=$gap; fi
    prev=$t
done
assert_eq "gaps between repair attempts never shrink (backoff)" "1" "$monotone"

# Recovery resets the backoff.
reset_sim; PROBE="1 activating 0 0 0 none"; run_ticks 6
PROBE="1 active 1 1 0 session"; run_ticks 3
assert_eq "restarts counter cleared after recovery" "0" "$(wd_state_get "$WD_STATE_DIR/main.state" restarts x)"
assert_eq "fail counter cleared after recovery" "0" "$(wd_state_get "$WD_STATE_DIR/main.state" fails x)"

# Black hole (the Turkey peer): session exists, data never ACKed.
reset_sim; PROBE="1 active 1 1 1 session"; run_ticks 4
assert_contains "black hole: first repair restarts the FRP service" "$(cat "$LOG")" "systemctl restart frpc"
assert_not_contains "black hole: first repair leaves GRE alone" "$(cat "$LOG")" "gre-tunnel.service"
reset_sim; PROBE="1 active 1 1 1 session"; run_ticks 40
assert_not_contains "a long black hole never flips the carrier by itself" "$(cat "$LOG")" "carrier_cycle_next"
assert_contains "...but it escalates to restarting GRE too" "$(cat "$LOG")" "systemctl restart gre-tunnel.service"
assert_contains "...and tells the operator about the WSS carrier" "$(wd_run_tick)" "HINT: the direct GRE path keeps failing"
reset_sim; PROBE="1 active 1 1 0 session"; run_ticks 5
assert_not_contains "a healthy tunnel produces no hint" "$(wd_run_tick)" "HINT:"

# Isolation: a broken peer must not restart the main tunnel (old code restarted everything).
reset_sim
TUNNELS="main|server|gre-tunnel|10.10.10.1|frps|39015|main
peer1|server|gre-t1|10.12.0.1|frps-1|7001|turkey"
PROBE="1 active 1 1 0 session"; PROBE_peer1="1 active 1 1 1 session"
run_ticks 6
assert_contains "broken peer: its own GRE+FRP restarted" "$(cat "$LOG")" "systemctl restart gre-t1.service"
assert_contains "broken peer: its own frps restarted" "$(cat "$LOG")" "systemctl restart frps-1"
assert_not_contains "broken peer: main GRE untouched" "$(cat "$LOG")" "gre-tunnel.service"
assert_not_contains "broken peer: main frps untouched" "$(cat "$LOG")" "restart frps"$'\n'
# the tick output (what Telegram alerts and the panel are built from)
reset_sim; TUNNELS="main|server|gre-tunnel|10.10.10.1|frps|39015|main
peer1|server|gre-t1|10.12.0.1|frps-1|7001|turkey"
PROBE="1 active 1 1 0 session"; PROBE_peer1="1 active 1 1 1 session"
tick1=$(wd_run_tick); CLOCK=$(( CLOCK + 60 ))
tick2=$(wd_run_tick)
assert_contains "1st bad tick only counts" "$tick1" "stuck(1/2)"
assert_not_contains "1st bad tick repairs nothing" "$tick1" "ACTIONS:"
assert_contains "2nd bad tick names the failing tunnel" "$tick2" "turkey(server): stuck"
assert_contains "2nd bad tick reports the repair" "$tick2" "ACTIONS: repaired turkey: RESTART_FRP"
assert_contains "a down summary is status=down" "$(printf '%s\n' "$tick2" | grep '^WATCHDOG')" "status=down"
assert_not_contains "a down summary never contains the substring the panel treats as up" \
    "$(printf '%s\n' "$tick2" | grep '^WATCHDOG')" "status=up"
unset PROBE_peer1

# Read-only check must not change state or restart anything.
reset_sim; TUNNELS="main|client|gre-tunnel|10.10.10.1|frpc|39015|main"
PROBE="1 inactive 0 0 0 none"
init_watchdog_json() { :; }
autotune_tick() { :; }
out=$(watchdog_check)
assert_contains "watchdog_check reports down for a dead link" "$out" "status=down"
assert_contains "watchdog_check explains why" "$out" "link-down"
assert_eq "watchdog_check performed no restart" "0" "$(restart_calls)"
assert_eq "watchdog_check wrote no state" "no" "$([[ -e "$WD_STATE_DIR/main.state" ]] && echo yes || echo no)"
PROBE="1 active 1 1 0 ping"
assert_contains "watchdog_check reports up when healthy" "$(watchdog_check)" "status=up"
TUNNELS=""
assert_contains "no tunnel configured is reported, not 'up'" "$(watchdog_check)" "status=down"

t_summary

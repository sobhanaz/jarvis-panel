#!/usr/bin/env bash
# Tests for the carrier helpers in hashem.sh: removed carriers must not come back,
# and legacy public FOU listeners must be cleaned up.
T_NAME="carrier"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/lib.sh
source "$HERE/lib.sh"
SCRIPT="${HASHEM_SCRIPT_UNDER_TEST:-$HERE/../hashem.sh}"
# shellcheck source=/dev/null
source "$SCRIPT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
CARRIER_FILE="$TMP/carrier.json"
LOG="$TMP/calls.log"

# never touch the real system
mkdir() { local a; for a in "$@"; do [[ "$a" == /etc/* || "$a" == /var/* ]] && return 0; done; command mkdir "$@"; }
modprobe() { :; }
iptables() { echo "iptables $*" >> "$LOG"; return 0; }
ufw() { return 1; }
sleep() { :; }
GRE_LINK_OUT=""
ip() {
    echo "ip $*" >> "$LOG"
    case "$*" in
        "-d link show type gre") printf '%s\n' "$GRE_LINK_OUT" ;;
        "-o link show type gre") echo "5: gre-tunnel@NONE: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1380" ;;
        "-o -4 addr show dev gre-tunnel") echo "5: gre-tunnel    inet 10.10.10.2/30 scope global gre-tunnel" ;;
    esac
    return 0
}
jget() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2], ""))' "$CARRIER_FILE" "$1"; }
fresh() { rm -f "$CARRIER_FILE" "$LOG"; : > "$LOG"; GRE_LINK_OUT=""; }

# ------------------------------------------------------------------------------
section "carrier_set_mode: removed modes are normalised to direct"
fresh; carrier_set_mode auto;       assert_eq "auto -> direct"        "direct"    "$(jget mode)"
fresh; carrier_set_mode fou:443;    assert_eq "fou:443 -> direct"     "direct"    "$(jget mode)"
fresh; carrier_set_mode wss:8443;   assert_eq "wss:8443 kept"         "wss:8443"  "$(jget mode)"
fresh; carrier_set_mode direct;     assert_eq "direct kept"           "direct"    "$(jget mode)"
fresh; carrier_set_mode bogus;      assert_eq "garbage is rejected (non-zero)" "1" "$?"

# ------------------------------------------------------------------------------
section "carrier_init_kernel: no public FOU listeners are created, leftovers are removed"
fresh; carrier_init_kernel
assert_not_contains "never adds a FOU listener" "$(cat "$LOG")" "ip fou add"
assert_contains "removes legacy UDP 443 listener"   "$(cat "$LOG")" "ip fou del port 443"
assert_contains "removes legacy UDP 55555 listener" "$(cat "$LOG")" "ip fou del port 55555"
assert_contains "removes legacy UDP 19998 listener" "$(cat "$LOG")" "ip fou del port 19998"
assert_not_contains "leaves firewall rules alone" "$(cat "$LOG")" "iptables"

fresh
GRE_LINK_OUT="    gre remote 1.2.3.4 local 5.6.7.8 ttl 255 encap fou encap-sport auto encap-dport 19998 noencap-csum"
carrier_init_kernel
assert_not_contains "keeps the listener a GRE device still encapsulates to" "$(cat "$LOG")" "ip fou del port 19998"
assert_contains "...while still removing the unused ones" "$(cat "$LOG")" "ip fou del port 443"

# a port that merely *starts with* the same digits must not count as in use
fresh
GRE_LINK_OUT="    gre remote 1.2.3.4 local 5.6.7.8 ttl 255 encap fou encap-sport auto encap-dport 44300"
carrier_init_kernel
assert_contains "port 44300 in use does not protect port 443" "$(cat "$LOG")" "ip fou del port 443"

# ------------------------------------------------------------------------------
section "carrier_apply: a removed carrier degrades to direct GRE, never to FOU encapsulation"
fresh
CARRIER_FILE="$TMP/carrier.json"; carrier_apply "fou:443" >/dev/null 2>&1
assert_contains "fou:443 is applied as 'encap none'" "$(cat "$LOG")" "type gre encap none"
assert_not_contains "no FOU encapsulation was configured" "$(cat "$LOG")" "encap fou"
assert_eq "recorded active carrier is direct" "direct" "$(jget active_carrier)"
fresh; carrier_apply "auto" >/dev/null 2>&1
assert_contains "'auto' is applied as 'encap none'" "$(cat "$LOG")" "type gre encap none"

# ------------------------------------------------------------------------------
section "carrier_cycle_next skips removed FOU candidates"
fresh
cat > "$CARRIER_FILE" <<'EOF'
{"mode":"direct","active_carrier":"direct","wss_port":8443,"candidates":["direct","fou:443","fou:55555","wss:8443"]}
EOF
carrier_apply() { echo "apply $1" >> "$LOG"; }
assert_eq "direct -> wss:8443 (not fou:443)" "wss:8443" "$(carrier_cycle_next)"
python3 - "$CARRIER_FILE" <<'PY'
import json, sys
p = sys.argv[1]; d = json.load(open(p)); d["active_carrier"] = "wss:8443"; json.dump(d, open(p, "w"))
PY
assert_eq "wss:8443 -> direct (wraps)" "direct" "$(carrier_cycle_next)"

t_summary

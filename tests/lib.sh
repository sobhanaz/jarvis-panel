#!/usr/bin/env bash
# Minimal assertion helpers for the bash test-suite.
# No dependencies; works on bash 3.2 (macOS) and bash 5 (CI / servers).

T_PASS=0
T_FAIL=0

ok()  { T_PASS=$((T_PASS + 1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad() {
    T_FAIL=$((T_FAIL + 1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"
    [[ -n "${2:-}" ]] && printf '       %s\n' "$2"
    return 0
}

assert_eq() { # desc expected actual
    if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "expected [$2] got [$3]"; fi
}

assert_contains() { # desc haystack needle
    if [[ "$2" == *"$3"* ]]; then ok "$1"; else bad "$1" "[$2] does not contain [$3]"; fi
}

assert_not_contains() { # desc haystack needle
    if [[ "$2" != *"$3"* ]]; then ok "$1"; else bad "$1" "[$2] unexpectedly contains [$3]"; fi
}

assert_le() { # desc actual max
    if (( $2 <= $3 )); then ok "$1 ($2 <= $3)"; else bad "$1" "expected <= $3, got $2"; fi
}

assert_ge() { # desc actual min
    if (( $2 >= $3 )); then ok "$1 ($2 >= $3)"; else bad "$1" "expected >= $3, got $2"; fi
}

section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

t_summary() {
    printf '\n%s: %d passed, %d failed\n' "${T_NAME:-tests}" "$T_PASS" "$T_FAIL"
    [[ $T_FAIL -eq 0 ]]
}

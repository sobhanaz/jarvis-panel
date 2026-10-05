#!/usr/bin/env bash
# Run every tests/test_*.sh in its own shell and report one overall result.
#   bash tests/run.sh            # all
#   bash tests/run.sh watchdog   # only tests/test_watchdog.sh
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
rc=0
for f in "$HERE"/test_${1:-*}.sh; do
    [[ -f "$f" ]] || continue
    printf '\n\033[1;36m== %s ==\033[0m\n' "$(basename "$f")"
    bash "$f" || rc=1
done
exit $rc

#!/usr/bin/env bash
# Usage: scripts/quarantine-check.sh list|check [root]
# The call sites of quarantine.Skip are the registry. `check` exits 1 on an
# expired quarantine (UTC date), a non-literal ref or date, a call outside a
# _test.go file, or a production binary that links the package.
set -euo pipefail

mode="${1:-list}"
root="${2:-$(git rev-parse --show-toplevel)}"
pkg="github.com/freeCodeCamp/artemis/internal/testutil/quarantine"
testutil="github.com/freeCodeCamp/artemis/internal/testutil/"
today="$(date -u +%F)"
bad=0

sites="$(grep -rnE 'quarantine\.Skip\(' --include='*.go' --exclude-dir=.scratchpad "$root" || true)"

if [ -z "$sites" ]; then
    echo "no quarantined tests"
else
    printf '%-60s %-16s %-11s %s\n' "site" "ref" "expires" "status"
    while IFS= read -r line; do
        file="${line%%:*}"; rest="${line#*:}"; lno="${rest%%:*}"; code="${rest#*:}"
        site="${file#"$root"/}:$lno"
        case "$file" in
            *_test.go) ;;
            *) printf '%-60s %s\n' "$site" "NOT-A-TEST-FILE"; bad=1; continue ;;
        esac
        if [[ "$code" =~ quarantine\.Skip\([^,]+,\ *\"([^\"]+)\",\ *\"([0-9]{4}-[0-9]{2}-[0-9]{2})\"\ *\) ]]; then
            ref="${BASH_REMATCH[1]}"; exp="${BASH_REMATCH[2]}"
            if [[ "$exp" < "$today" ]]; then status="EXPIRED"; bad=1; else status="active"; fi
            printf '%-60s %-16s %-11s %s\n' "$site" "$ref" "$exp" "$status"
        else
            printf '%-60s %s\n' "$site" "NON-LITERAL-ARGS"; bad=1
        fi
    done <<< "$sites"
fi

aliases="$(grep -rnE "^\s*(import\s+)?[A-Za-z_][A-Za-z0-9_]*\s+\"$pkg\"" --include='*_test.go' --exclude-dir=.scratchpad "$root" | grep -vE ':[0-9]+:\s*((import\s+)?quarantine\s|import\s+")' || true)"
if [ -n "$aliases" ]; then
    printf '%s\n' "$aliases" | sed "s|^$root/||; s|$| ALIASED-IMPORT-HIDES-CALLS|"; bad=1
fi

if [ -f "$root/go.mod" ]; then
    deps="$(cd "$root" && go list -deps ./cmd/...)"
    if printf '%s\n' "$deps" | grep -q "^$testutil"; then
        echo "production binary links $testutil..."; bad=1
    fi
fi

if [ "$mode" = "check" ] && [ "$bad" -ne 0 ]; then
    echo "quarantine check failed: fix the test, extend the date in the same commit as the fix plan, or remove the call"
    exit 1
fi

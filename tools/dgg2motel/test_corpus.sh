#!/usr/bin/env bash
# Test motel against a corpus of DGG-generated topologies.
#
# Usage:
#   ./tools/dgg2motel/test_corpus.sh /path/to/topologies/
#
# Runs motel check and motel run on every YAML file in the directory tree.
# Reports failures and collects statistics.
set -euo pipefail

TOPO_DIR="${1:?usage: test_corpus.sh <topology-dir>}"
MOTEL="${MOTEL:-build/motel}"
DURATION="${DURATION:-1s}"
RATE="${RATE:-}"  # override traffic rate if set

if [ ! -x "$MOTEL" ]; then
    echo "error: $MOTEL not found or not executable" >&2
    exit 1
fi

temp_topo=""
cleanup() { [ -n "$temp_topo" ] && rm -f "$temp_topo"; }
trap cleanup EXIT INT TERM

check_pass=0
check_fail=0
run_pass=0
run_fail=0

files=()
while IFS= read -r f; do
    files+=("$f")
done < <(find "$TOPO_DIR" -name '*.yaml' | sort)

echo "testing ${#files[@]} topologies from $TOPO_DIR"
echo "motel: $MOTEL"
echo "duration: $DURATION"
echo ""

for f in "${files[@]}"; do
    rel="${f#"$TOPO_DIR"/}"

    # motel check
    if ! "$MOTEL" check "$f" > /dev/null 2>&1; then
        echo "CHECK FAIL: $rel"
        "$MOTEL" check "$f" 2>&1 | sed 's/^/  /'
        check_fail=$((check_fail + 1))
    else
        check_pass=$((check_pass + 1))
    fi

    # motel run
    topo="$f"
    if [ -n "$RATE" ]; then
        temp_topo=$(mktemp /tmp/dgg-topo-XXXXXX.yaml)
        sed "s|rate: .*|rate: $RATE|" "$f" > "$temp_topo"
        topo="$temp_topo"
    fi

    output=$(timeout 10 "$MOTEL" run --stdout --duration "$DURATION" "$topo" 2>&1) || rc=$?
    rc=${rc:-0}

    if [ -n "$temp_topo" ]; then
        rm -f "$temp_topo"
        temp_topo=""
    fi

    if [ "$rc" -ne 0 ] && [ "$rc" -ne 124 ]; then
        echo "RUN FAIL ($rc): $rel"
        echo "$output" | tail -5 | sed 's/^/  /'
        run_fail=$((run_fail + 1))
    else
        run_pass=$((run_pass + 1))
    fi
done

echo ""
echo "=== results ==="
echo "check: $check_pass pass, $check_fail fail"
echo "run:   $run_pass pass, $run_fail fail"

if [ "$check_fail" -gt 0 ] || [ "$run_fail" -gt 0 ]; then
    exit 1
fi

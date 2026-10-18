#!/bin/sh
# Build, start two fake workers and the proxy, run the scenario, print the report.
set -e
cd "$(dirname "$0")"
command -v go >/dev/null || PATH="$HOME/sdk/go/bin:$PATH"

go build -o bin/ ./cmd/...
mkdir -p run
rm -f run/*.log run/*.jsonl

cleanup() {
    for f in run/worker0.pid run/worker1.pid run/proxy.pid; do
        [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null || true
    done
}
trap cleanup EXIT

bin/fakeworker -port 8001 -pidfile run/worker0.pid >>run/worker0.log 2>&1 &
bin/fakeworker -port 8002 -pidfile run/worker1.pid >>run/worker1.log 2>&1 &
bin/proxy >>run/proxy.log 2>&1 &
echo $! > run/proxy.pid

bin/harness run --scenario "${1:-scenarios/fake-2x.yaml}"
echo
bin/harness report report.json --format md

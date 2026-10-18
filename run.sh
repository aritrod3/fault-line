#!/bin/sh
# Build, start the fake stack, record a baseline, run the scenario, print the report.
# Topology: proxy -> worker0, and proxy -> corrupter -> worker1.
set -e
cd "$(dirname "$0")"
command -v go >/dev/null || PATH="$HOME/sdk/go/bin:$PATH"

go build -o bin/ ./cmd/...
mkdir -p run
rm -f run/*.log run/*.jsonl

cleanup() {
    for f in run/worker0.pid run/worker1.pid run/corrupter1.pid run/proxy.pid; do
        [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null || true
    done
}
trap cleanup EXIT

bin/fakeworker -port 8001 -pidfile run/worker0.pid >>run/worker0.log 2>&1 &
bin/fakeworker -port 8002 -pidfile run/worker1.pid >>run/worker1.log 2>&1 &
bin/corrupter -listen 127.0.0.1:8012 -upstream http://127.0.0.1:8002 -pidfile run/corrupter1.pid >>run/corrupter1.log 2>&1 &
bin/proxy -backends http://127.0.0.1:8001,http://127.0.0.1:8012 >>run/proxy.log 2>&1 &
echo $! > run/proxy.pid

scenario="${1:-scenarios/fake-2x.yaml}"
bin/harness baseline --scenario "$scenario"
bin/harness run --scenario "$scenario"
echo
bin/harness report report.json --format md

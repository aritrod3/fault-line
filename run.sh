#!/bin/sh
# v0.1 end-to-end: build, start two fake workers and the proxy, run the
# harness, then stop everything.
set -e
cd "$(dirname "$0")"
command -v go >/dev/null || PATH="$HOME/sdk/go/bin:$PATH"

go build -o bin/ ./cmd/...
mkdir -p run
rm -f run/*.log run/requests.jsonl

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

echo "waiting for workers to load..."
until curl -sf http://127.0.0.1:8001/health >/dev/null && curl -sf http://127.0.0.1:8002/health >/dev/null; do
    sleep 0.5
done
sleep 2 # let the proxy's health loop see both as up

bin/harness

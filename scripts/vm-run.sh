#!/bin/sh
# run.sh for a real stack on a GPU VM. Usage: vm-run.sh vllm|sglang [scenario]
# Two workers on one GPU, proxy, corrupter, slowfs, then baseline, run and report.
# Topology: proxy -> worker0, proxy -> corrupter -> worker1.
# Both workers read weights through slowfs (run/hf).
set -e
cd "$(dirname "$0")/.."
stack="${1:-vllm}"
command -v go >/dev/null || PATH="$HOME/sdk/go/bin:$PATH"

go build -buildvcs=false -o bin/ ./cmd/...
mkdir -p run
rm -f run/*.log run/*.jsonl

cleanup() {
    for f in "run/${stack}0.pid" "run/${stack}1.pid"; do
        [ -f "$f" ] && kill -- "-$(cat "$f")" 2>/dev/null || true
    done
    for f in run/corrupter1.pid run/proxy.pid run/slowfs.pid; do
        [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null || true
    done
    sleep 1
    rmdir /sys/fs/cgroup/faultline-w0 /sys/fs/cgroup/faultline-w1 2>/dev/null || true
}
trap cleanup EXIT

bin/slowfs -src "$HOME/.cache/huggingface" -mnt "$PWD/run/hf" -pidfile run/slowfs.pid >>run/slowfs.log 2>&1 &
until mountpoint -q run/hf; do sleep 0.2; done

setsid "scripts/$stack-worker.sh" 0 8001 >>"run/${stack}0.log" 2>&1 &
setsid "scripts/$stack-worker.sh" 1 8002 >>"run/${stack}1.log" 2>&1 &
bin/corrupter -listen 127.0.0.1:8012 -upstream http://127.0.0.1:8002 -pidfile run/corrupter1.pid >>run/corrupter1.log 2>&1 &
bin/proxy -backends http://127.0.0.1:8001,http://127.0.0.1:8012 >>run/proxy.log 2>&1 &
echo $! > run/proxy.pid

scenario="${2:-scenarios/$stack-2x.yaml}"
bin/harness baseline --scenario "$scenario"
bin/harness run --scenario "$scenario"
echo
bin/harness report report.json --format md

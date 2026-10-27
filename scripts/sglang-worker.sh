#!/bin/sh
# Start one SGLang worker and write its PID. Usage: sglang-worker.sh N PORT
# Start it as a process-group leader (setsid) so faults reach its child processes too.
cd "$(dirname "$0")/.."
echo $$ > "run/sglang$1.pid"

# Own cgroup, for memory_pressure (needs root).
cg="/sys/fs/cgroup/faultline-w$1"
{ mkdir -p "$cg" && echo $$ > "$cg/cgroup.procs"; } 2>/dev/null || echo "no cgroup for worker $1" >&2

# Weights are read through the slowfs mount, for storage_stall.
export HF_HOME="$PWD/run/hf" HF_HUB_OFFLINE=1

# SGLang's kernel compiler needs the venv's tools (ninja) on PATH.
PY="${SGLANG_PY:-$HOME/sglang-venv/bin/python}"
export PATH="$(dirname "$PY"):$PATH"

# SGLang's memory fraction counts what is already in use on the GPU. Worker 1
# starts first with 40%; worker 0 starts (and restarts) next to it, so 80% leaves it the other 40%.
frac=0.4
[ "$1" = 0 ] && frac=0.8

exec "$PY" -m sglang.launch_server --model-path "${MODEL:-Qwen/Qwen2.5-0.5B-Instruct}" \
    --host 127.0.0.1 --port "$2" --random-seed 0 \
    --mem-fraction-static "$frac" --context-length 2048

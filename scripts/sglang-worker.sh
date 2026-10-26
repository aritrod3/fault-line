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

exec "$PY" -m sglang.launch_server --model-path "${MODEL:-Qwen/Qwen2.5-0.5B-Instruct}" \
    --host 127.0.0.1 --port "$2" --random-seed 0 \
    --mem-fraction-static 0.4 --context-length 2048

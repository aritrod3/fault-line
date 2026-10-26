#!/bin/sh
# Start one vLLM worker and write its PID. Usage: vllm-worker.sh N PORT
# Start it as a process-group leader (setsid) so faults reach its engine process too.
cd "$(dirname "$0")/.."
echo $$ > "run/vllm$1.pid"

# Own cgroup, for memory_pressure (needs root).
cg="/sys/fs/cgroup/faultline-w$1"
{ mkdir -p "$cg" && echo $$ > "$cg/cgroup.procs"; } 2>/dev/null || echo "no cgroup for worker $1" >&2

# Weights are read through the slowfs mount, for storage_stall.
export HF_HOME="$PWD/run/hf" HF_HUB_OFFLINE=1

# vLLM's kernel compiler needs the venv's tools (ninja) on PATH.
VLLM="${VLLM:-$HOME/vllm-venv/bin/vllm}"
export PATH="$(dirname "$VLLM"):$PATH"

exec "$VLLM" serve "${MODEL:-Qwen/Qwen2.5-0.5B-Instruct}" \
    --host 127.0.0.1 --port "$2" --seed 0 \
    --gpu-memory-utilization 0.4 --max-model-len 2048

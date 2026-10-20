#!/bin/sh
# Start one vLLM worker and write its PID. Usage: vllm-worker.sh N PORT
# Start it as a process-group leader (setsid) so faults reach its engine process too.
cd "$(dirname "$0")/.."
echo $$ > "run/vllm$1.pid"
exec "${VLLM:-$HOME/vllm-venv/bin/vllm}" serve "${MODEL:-Qwen/Qwen2.5-0.5B-Instruct}" \
    --host 127.0.0.1 --port "$2" --seed 0 \
    --gpu-memory-utilization 0.4 --max-model-len 2048

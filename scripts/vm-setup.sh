#!/bin/sh
# One-time setup on an Ubuntu GPU VM: Go, vLLM in a venv, and the model weights.
set -e
GO=go1.27.2
MODEL="${MODEL:-Qwen/Qwen2.5-0.5B-Instruct}"

sudo apt-get update -q
sudo apt-get install -y -q python3-venv iproute2 curl

arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
file="$GO.linux-$arch.tar.gz"
sum=$(curl -sSf "https://go.dev/dl/?mode=json&include=all" |
    python3 -c "import json,sys; print(next(f['sha256'] for r in json.load(sys.stdin) for f in r['files'] if f['filename']=='$file'))")
mkdir -p "$HOME/sdk" && cd "$HOME/sdk"
curl -sSfLO "https://go.dev/dl/$file"
echo "$sum  $file" | sha256sum -c
rm -rf go && tar -xzf "$file" && rm "$file"
grep -qs 'sdk/go/bin' "$HOME/.profile" || echo 'export PATH="$HOME/sdk/go/bin:$PATH"' >> "$HOME/.profile"

python3 -m venv "$HOME/vllm-venv"
"$HOME/vllm-venv/bin/pip" install -q vllm
"$HOME/vllm-venv/bin/python" -c "from huggingface_hub import snapshot_download; snapshot_download('$MODEL')"

echo "vLLM $("$HOME/vllm-venv/bin/vllm" --version) ready; set it as version in scenarios/vllm-2x.yaml"

#!/bin/sh
# One-time setup on an Ubuntu GPU VM: Go, each stack in its own venv, and the model weights.
set -e
GO=go1.27.2
MODEL="${MODEL:-Qwen/Qwen2.5-0.5B-Instruct}"

sudo apt-get -o DPkg::Lock::Timeout=900 update -q
sudo apt-get -o DPkg::Lock::Timeout=900 install -y -q python3-venv python3-dev iproute2 curl fuse3

arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
file="$GO.linux-$arch.tar.gz"
sum=$(curl -sSf "https://go.dev/dl/?mode=json&include=all" |
    python3 -c "import json,sys; print(next(f['sha256'] for r in json.load(sys.stdin) for f in r['files'] if f['filename']=='$file'))")
mkdir -p "$HOME/sdk" && cd "$HOME/sdk"
curl -sSfLO "https://go.dev/dl/$file"
echo "$sum  $file" | sha256sum -c
rm -rf go && tar -xzf "$file" && rm "$file"
grep -qs 'sdk/go/bin' "$HOME/.profile" || echo 'export PATH="$HOME/sdk/go/bin:$PATH"' >> "$HOME/.profile"

# Each stack gets its own venv. Usage: vm-setup.sh [vllm] [sglang] (default: both)
for stack in ${*:-vllm sglang}; do
    python3 -m venv "$HOME/$stack-venv"
    "$HOME/$stack-venv/bin/pip" install -q "$stack"
    v=$("$HOME/$stack-venv/bin/python" -c "import $stack; print($stack.__version__)")
    echo "$stack $v ready; set it as version in scenarios/$stack-2x.yaml"
done
"$HOME/$stack-venv/bin/python" -c "from huggingface_hub import snapshot_download; snapshot_download('$MODEL')"

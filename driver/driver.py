"""Load driver: keep N requests in flight for a fixed duration.

Prompts are sent in a fixed cycle at temperature 0 with a fixed seed. Each
request's timing, success, prompt index and response text are kept in
memory and written to a JSONL file after the run, so logging never sits
in the request path.
"""
import argparse
import itertools
import json
import threading
import time
import urllib.request


def worker(args, prompts, counter, deadline, records, lock):
    local = []
    while time.time() < deadline:
        i = next(counter) % len(prompts)
        body = json.dumps({
            "model": args.model, "prompt": prompts[i],
            "max_tokens": 16, "temperature": 0, "seed": 0,
        }).encode()
        start = time.time()
        ok, text = False, None
        try:
            req = urllib.request.Request(args.url, data=body, headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=10) as resp:
                text = json.loads(resp.read())["choices"][0]["text"]
                ok = resp.status == 200
        except Exception:
            pass
        local.append({"start": start, "end": time.time(), "ok": ok, "prompt": i, "text": text})
    with lock:
        records.extend(local)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--url", required=True)
    p.add_argument("--prompts", required=True)
    p.add_argument("--model", default="")
    p.add_argument("--concurrency", type=int, default=8)
    p.add_argument("--duration", type=float, default=60)
    p.add_argument("--out", required=True)
    args = p.parse_args()

    with open(args.prompts) as f:
        prompts = [json.loads(line)["prompt"] for line in f if line.strip()]
    counter = itertools.count()
    deadline = time.time() + args.duration
    records, lock = [], threading.Lock()
    threads = [
        threading.Thread(target=worker, args=(args, prompts, counter, deadline, records, lock))
        for _ in range(args.concurrency)
    ]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    with open(args.out, "w") as f:
        for r in sorted(records, key=lambda r: r["start"]):
            f.write(json.dumps(r) + "\n")
    print("driver: %d requests, %d failed" % (len(records), sum(not r["ok"] for r in records)))


if __name__ == "__main__":
    main()

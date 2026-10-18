"""Load driver: keep N requests in flight for a fixed duration.

Each request's start time, end time and success are kept in memory and
written to a JSONL file after the run, so logging never sits in the
request path.
"""
import argparse
import json
import threading
import time
import urllib.request

BODY = json.dumps({"prompt": "Say hello.", "max_tokens": 16, "temperature": 0}).encode()


def worker(url, deadline, records, lock):
    local = []
    while time.time() < deadline:
        start = time.time()
        ok = False
        try:
            req = urllib.request.Request(url, data=BODY, headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=10) as resp:
                resp.read()
                ok = resp.status == 200
        except Exception:
            pass
        local.append({"start": start, "end": time.time(), "ok": ok})
    with lock:
        records.extend(local)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--url", required=True)
    p.add_argument("--concurrency", type=int, default=8)
    p.add_argument("--duration", type=float, default=60)
    p.add_argument("--out", required=True)
    args = p.parse_args()

    deadline = time.time() + args.duration
    records, lock = [], threading.Lock()
    threads = [
        threading.Thread(target=worker, args=(args.url, deadline, records, lock))
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

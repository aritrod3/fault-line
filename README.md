# faultline

faultline breaks a running LLM serving stack on purpose and reports how it fails and how it recovers.

## Result

For each fault, faultline reports five numbers: detection time, recovery time, requests lost, requests corrupted, and degradation.

Results for vLLM are pending. The numbers produced so far come from a stand-in worker, which only tests the harness itself. They are not results.

## How it works

Three parts sit outside the stack:

- **Driver** sends a fixed set of prompts at a steady concurrency and logs every request.
- **Injector** applies one fault at a time through the operating system, then undoes it.
- **Recorder** computes the metrics from the driver's log after the run.

The stack is a black box. Nothing in it is modified. Faults are applied with signals, `tc`, FUSE and cgroups, never with application hooks.

## Faults

| Fault | What it does | Undo |
| --- | --- | --- |
| `kill_backend` | SIGKILL the worker | Restart it |
| `hang_backend` | SIGSTOP the worker | SIGCONT |
| `slow_network` | `tc netem` delay and loss on the worker's port | Remove the qdisc |
| `storage_stall` | Slow every read on the weight mount, drop the page cache, restart the worker | Remove the delay |
| `memory_pressure` | Set the worker's cgroup `memory.max` below its usage | Reset to `max` |
| `corrupt_output` | A proxy in front of the worker flips one bit in each completion | Stop flipping |

After every undo, the worker is restarted if it is not running.

Each fault runs alone, in its own window. Faults are never combined.

## Metrics

Baseline throughput is the mean per-second count of successful requests from second 5 until the fault starts.

| Metric | Rule |
| --- | --- |
| Detection | Time from injection to the first failed request, or to the first request that has taken twice the baseline p99 |
| Recovery | Time from clearing the fault until the 3-second rolling mean of throughput stays at or above 95% of baseline for 10 seconds |
| Lost | Requests that failed or timed out after injection |
| Corrupted | Successful responses after injection that differ from the baseline |
| Degradation | Throughput and p99 latency during the fault, as fractions of baseline |

Throughput is counted per second, so recovery is only accurate to about one second.

## Why the corruption count holds

A response counts as corrupted only if it differs from a known-good answer to the same prompt.

1. Every request uses temperature 0 and a fixed seed.
2. `harness baseline` runs the normal load, with no fault, and records every answer to every prompt.
3. A prompt that got more than one distinct answer is marked nondeterministic and is never checked.
4. Every other prompt has exactly one known-good answer.
5. During a run, only successful responses that end after injection are compared.

So a difference cannot come from normal variation, because prompts that vary under normal load are excluded. It can only come from the fault.

What it cannot see:

- Corruption on a nondeterministic prompt.
- Corruption that produces the same text as the baseline.
- A stack that was already wrong when the baseline was recorded.

The baseline reports how many prompts were nondeterministic. If most of them are, the corruption count means little.

## Crash safety

Before a fault is applied, its undo is written to `run/fault.journal`. The undo runs when the fault clears, when the harness exits, and on SIGINT or SIGTERM. If the harness dies anyway, the next run replays the journal before doing anything else.

## Running it

**Stand-in stack, on macOS or Linux.** Needs Go and Python 3.

```bash
./run.sh
```

Linux-only faults report an error on macOS. They are not skipped silently.

**vLLM, on a Linux VM with an NVIDIA GPU, as root.**

```bash
scripts/vm-setup.sh
scripts/vllm-run.sh
```

Set `version` in `scenarios/vllm-2x.yaml` before running.

Each run writes `report.json`. It records the stack, version, model, host, GPU, the scenario exactly as run, and the baseline. To render it:

```bash
bin/harness report report.json --format md
```

## Limitations

- **Router.** The router is a small round-robin proxy written for this project, not a production router.
- **Single host.** Network faults run on loopback, filtered to one worker's port.
- **Memory.** cgroups limit host memory only. GPU memory is not affected.
- **Precision.** Recovery time is measured to the nearest second.

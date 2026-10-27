# faultline

faultline breaks a running LLM serving stack on purpose and reports how it fails and how it recovers.

## Result

For each fault, faultline reports five numbers: detection time, recovery time, requests lost, requests corrupted, and degradation.

**Setup.** vLLM 0.31.0 and SGLang 0.5.21, each with Qwen2.5-0.5B-Instruct, two workers on one NVIDIA H100, the same faults and proxy, 64 requests in flight, 3 runs. Each cell is the median of the 3 runs, vLLM first, with the range in brackets where it varied. "Never" means not within the 3-minute window.

| Fault | Recovery | Lost | Throughput | p99 |
| --- | --- | --- | --- | --- |
| `kill_backend` | 27s (26 to 32) / 25s (24 to 25) | 33 / 32 | 0.78× / 0.89× | 1.3× / 0.9× |
| `hang_backend` | 1s (1 to 27) / 33s (13 to never) | 50 / 55 | 0.00× / 0.00× | 85× / 56× |
| `slow_network` | 3s (1 to 6) / 13s (5 to 46) | 0 / 0 | 0.34× / 0.42× | 11× / 7.4× |
| `corrupt_output` | 1s (1 to 3) / 1s (1 to 5) | 0 / 0 | 1.01× / 1.03× | 1.0× / 1.0× |
| `storage_stall` | 14s (14 to 15) / 15s (11 to never) | 34 / 32 | 0.77× / 0.87× | 1.3× / 1.0× |
| `memory_pressure` | 2s (1 to 4) / 6s (1 to 26) | 0 / 0 | 1.00× / 0.97× | 1.1× / 1.0× |

Corrupted responses under `corrupt_output`: 1456 (1287 to 1741) for vLLM, 1362 (951 to 1423) for SGLang. Under every other fault: 0 to 4.

Full reports, baselines and logs are in [`results/vllm-h100/`](results/vllm-h100/) and [`results/sglang-h100/`](results/sglang-h100/).

## Findings

- **Silent corruption is invisible to the stack.** About 1,400 wrong answers per run were returned with no error, no slowdown and no reaction from either stack.
- **A hung worker stops all traffic.** On both stacks, throughput fell to zero, because stuck requests filled every slot. A dead worker is less harmful: throughput only fell to about 0.8×.
- **The two stacks fail almost the same way.** Both lose about 30 requests when a worker dies, and both restart it in about 25 seconds.
- **Determinism breaks under load.** At 64 requests in flight, only 6 to 9 of 16 prompts gave the same answer every time, even at temperature 0 with a fixed seed. At 8 in flight, all 16 did (vLLM).
- **A restarted vLLM worker answers differently.** At 8 in flight, where answers are otherwise stable, the restarted worker returned 74 changed answers after a kill and 54 after a storage stall. At 64 in flight the effect is lost in the noise. SGLang was only run at 64, so this was not tested for it.
- **SGLang handles slow network better, but recovers less consistently.** Its p99 rose 7.4×, against 11× for vLLM. But its recovery ranged from 5 to 46 seconds, and twice a fault did not recover within the window.
- **The memory fault has no effect.** Both stacks keep their memory on the GPU, and cgroups only limit host memory.

## Light load

One run used 8 requests in flight. At that load, one worker could absorb all the traffic, so killing the other one changed nothing and most faults looked harmless. That run is kept in [`results/vllm-h100/c8-run1/`](results/vllm-h100/c8-run1/), but the main results use 64.

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

**vLLM or SGLang, on a Linux VM with an NVIDIA GPU, as root.**

```bash
scripts/vm-setup.sh vllm
scripts/vm-run.sh vllm
```

Use `sglang` in place of `vllm` for SGLang. Setup prints the installed version: set it as `version` in `scenarios/vllm-2x.yaml` or `scenarios/sglang-2x.yaml` before running.

Each run writes `report.json`. It records the stack, version, model, host, GPU, the scenario exactly as run, and the baseline. To render it:

```bash
bin/harness report report.json --format md
```

## Limitations

- **Router.** The router is a small round-robin proxy written for this project, not a production router.
- **Single host.** Network faults run on loopback, filtered to one worker's port.
- **Memory.** cgroups limit host memory only. GPU memory is not affected.
- **Precision.** Recovery time is measured to the nearest second.
- **Noise.** Corruption counts of 1 to 4 outside `corrupt_output` are noise: a few answers that vary under load.
- **Runs.** There are only 3 runs per stack. Hang recovery varied widely between them.
- **Setup differences.** SGLang used about 73 GB of GPU memory, against 63 GB for vLLM. SGLang's liveness check uses `/v1/models`, because its `/health` runs a generation and takes about 1 second.

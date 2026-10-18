# faultline: Fault Injection Harness for AI Serving Stacks

**Spec v0.1** · Oct 15, 2026 · @Aritro Datta

## Goal

Break a running LLM serving stack on purpose, and report whether it recovered, how long it took, and whether anything failed silently.

The claim the tool makes is a table: for a given stack, here are N failure modes, and for each one the recovery time and the number of requests lost or corrupted. Nothing in the project matters except producing that table honestly.

## Non-goals

Scope stays small on purpose. This is not:

- A Kubernetes operator. Single host, or a few hosts over SSH. No cluster orchestration.
- A load testing tool. It drives just enough traffic to observe behaviour under fault, not to find peak throughput.
- A monitoring system. It writes a report at the end of a run and exits. No dashboards, no time-series database.
- A general chaos framework. It targets LLM serving stacks specifically, and the fault catalog is hand-picked rather than extensible on day one.

## Architecture

_(architecture diagram: Driver, Injector and Recorder sit outside the stack; only the Injector reaches in, at the OS level, to the workers and the weight mount.)_

The driver and recorder sit outside the serving stack and treat it as a black box. Only the injector reaches in, and it reaches into the operating system rather than the application, which is what keeps the stack unmodified and the results honest.

The injector is the only component that can leave the machine in a bad state, so every fault must be reversible and reversal must run even when the harness crashes.

## Fault catalog (v1)

Six faults. Each is cheap to implement and maps to a real production incident. Ordered by implementation difficulty.

| # | Fault | Mechanism | Why it matters |
| --- | --- | --- | --- |
| 1 | Backend dies | SIGKILL the worker process | Baseline. Does the router notice and stop routing to it? |
| 2 | Backend hangs | SIGSTOP the worker process | Harder than death: the socket stays open, so naive health checks pass |
| 3 | Slow network | `tc netem` delay and loss on the worker's interface | Straggler nodes degrade the whole batch, not just one request |
| 4 | Storage stall | FUSE passthrough mount that sleeps on read | Hits during weight load; tests startup paths nobody exercises |
| 5 | Memory pressure | cgroup v2 `memory.max` tightened at runtime | Triggers the OOM path without needing a real GPU |
| 6 | Corrupt output | Proxy rewrites tokens in the response stream | The only fault where the system looks healthy and is wrong |

Fault 6 is the differentiator and should not be dropped for schedule. Faults 1 to 5 test whether failures are detected; fault 6 tests whether the stack can be wrong without anyone knowing, which is the question nobody else asks.

## What gets measured

Five numbers per fault. Everything else is noise.

| Metric | Definition |
| --- | --- |
| Detection time | Fault injected to first observable reaction (error, reroute, log line) |
| Recovery time | Fault cleared to steady-state throughput restored |
| Requests lost | In-flight requests that never returned a response |
| Requests corrupted | Responses returned that differ from the known-good baseline |
| Degradation | Throughput and p99 latency during fault, as a fraction of baseline |

Corruption detection needs a baseline run with temperature set to zero and a fixed seed, so the same prompts produce the same outputs. Any divergence under fault is then attributable. This is the one measurement with a real correctness argument behind it, and it should be documented carefully in the README.

Every run records the stack version, model, hardware, and fault parameters alongside the results. A number without its configuration is not a result.

## Interface

Three commands. A scenario is a YAML file so runs are reproducible and diffable in git.

```
harness baseline --scenario vllm-2x.yaml     # record known-good behaviour
harness run --scenario vllm-2x.yaml           # run every fault, write report.json
harness report report.json --format md        # render the table
```

Scenario file:

```yaml
target:
  router: http://localhost:8000
  workers:
    - pid_file: /run/vllm-0.pid
      iface: veth0
load:
  prompts: prompts.jsonl
  concurrency: 32
  duration: 60s
faults:
  - type: kill_backend
    target: workers[0]
    at: 20s
    duration: 10s
```

One fault per run by default. Combining faults is a later feature and a different, harder claim.

## Milestones

Each stage is publishable on its own. Stop at any point and the project still reads as finished work.

1. **v0.1 — One fault, one number.** vLLM with two workers behind a round-robin proxy you write yourself. Kill a worker, measure recovery time, print it. No config file, no report format.
2. **v0.2 — Scenario files and the report.** YAML input, JSON output, markdown rendering. Faults 1 to 3.
3. **v0.3 — Baseline and corruption detection.** Deterministic baseline runs, response diffing, fault 6.
4. **v0.4 — Storage and memory faults.** FUSE mount and cgroup manipulation. Faults 4 and 5.
5. **v1.0 — A second stack.** Run the identical catalog against SGLang and publish the comparison. This is the milestone that makes the project interesting to other people rather than just to you.

The comparison at v1.0 is the real artifact. Two mature systems, one fault catalog, side-by-side results is a blog post people will read and a conversation starter in any infrastructure interview.

## Open questions

- Write your own router or instrument an existing one? Writing a trivial round-robin proxy makes v0.1 reachable in a weekend, but testing a real router (SGLang's, llm-d's) is a stronger result. Probably: own proxy for v0.1, real router by v0.3.
- How is recovery defined precisely? Throughput back to 95% of baseline, sustained for 10 seconds, is a reasonable starting definition but needs a sanity check against noisy runs.
- Does determinism actually hold? Temperature zero plus a fixed seed should give reproducible output, but continuous batching may introduce nondeterminism through varying batch composition. Worth testing before committing to the corruption metric.
- Single host or multi-host? Multi-host is more realistic and makes network faults meaningful, but adds SSH orchestration. Possibly simulate with network namespaces on one machine instead.

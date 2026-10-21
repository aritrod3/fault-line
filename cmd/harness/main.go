// harness runs each fault in a scenario against a live stack and writes a
// report; `harness report` renders that report as a markdown table.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

var driver atomic.Pointer[exec.Cmd]

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "baseline":
		err = cmdBaseline(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: harness baseline --scenario FILE [--out baseline.json] [--duration 20s]
       harness run --scenario FILE [--baseline baseline.json] [--out report.json]
       harness report FILE [--format md]`)
	os.Exit(2)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	path := fs.String("scenario", "", "scenario YAML")
	basePath := fs.String("baseline", "baseline.json", "baseline from `harness baseline`")
	out := fs.String("out", "report.json", "report path")
	fs.Parse(args)
	sc, raw, err := loadScenario(*path)
	if err != nil {
		return err
	}
	os.MkdirAll("run", 0o755)

	var base *Baseline
	var ref map[int]string
	if data, err := os.ReadFile(*basePath); err != nil {
		log.Printf("no baseline, corruption not measured: %v", err)
	} else if err := json.Unmarshal(data, &base); err != nil {
		return err
	} else if len(base.Responses) > 0 {
		ref = base.Responses
	}

	reverse() // left over from a crashed run
	defer reverse()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Printf("got %v", <-sigs)
		reverse()
		if d := driver.Load(); d != nil {
			d.Process.Kill()
		}
		os.Exit(1)
	}()

	host, _ := os.Hostname()
	rep := Report{
		Stack: sc.Stack, Version: sc.Version, Model: sc.Model,
		Host:     Host{Name: host, OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), GPU: gpu(), Go: runtime.Version()},
		Started:  time.Now().UTC(),
		Scenario: string(raw),
		Baseline: base,
	}
	for i, f := range sc.Faults {
		log.Printf("fault %d/%d: %s on %s", i+1, len(sc.Faults), f.Type, f.Target)
		res := runFault(sc, i, f, ref)
		if res.Error != "" {
			log.Printf("  error: %s", res.Error)
		}
		rep.Results = append(rep.Results, res)
	}

	data, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	log.Printf("wrote %s", *out)
	return nil
}

func runFault(sc *Scenario, i int, f Fault, ref map[int]string) Result {
	res := Result{Fault: f.Type, Target: f.Target, AtS: f.At.Seconds(), DurationS: f.Duration.Seconds(), Netem: f.Netem, MemoryFrac: f.MemoryFrac}
	fail := func(err error) Result {
		res.Error = err.Error()
		return res
	}
	w, _ := sc.worker(f.Target)
	if runtime.GOOS != "linux" {
		switch f.Type {
		case "slow_network", "storage_stall", "memory_pressure":
			return fail(errors.New("needs Linux"))
		}
	}
	if err := waitHealthy(sc.Target.Workers, 5*time.Minute); err != nil {
		return fail(err)
	}

	reqLog := fmt.Sprintf("run/requests-%d.jsonl", i)
	t0 := time.Now()
	d, err := startDriver(sc, sc.Load.Duration, reqLog)
	if err != nil {
		return fail(err)
	}

	time.Sleep(f.At - time.Since(t0))
	inject := time.Now()
	if err := applyFault(f, w); err != nil {
		d.Process.Kill()
		d.Wait()
		return fail(err)
	}
	time.Sleep(f.At + f.Duration - time.Since(t0))
	clear := time.Now()
	reverse()

	if err := d.Wait(); err != nil {
		return fail(fmt.Errorf("driver: %w", err))
	}
	m, err := analyze(reqLog, t0, inject, clear, sc.Load.Duration, ref)
	if err != nil {
		return fail(err)
	}
	res.Metrics = m
	return res
}

func gpu() string {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader").Output()
	if err != nil {
		return "none"
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

func startDriver(sc *Scenario, dur time.Duration, out string) (*exec.Cmd, error) {
	d := exec.Command("python3", "driver/driver.py",
		"--url", sc.Target.Router,
		"--prompts", sc.Load.Prompts,
		"--model", sc.Model,
		"--concurrency", strconv.Itoa(sc.Load.Concurrency),
		"--duration", fmt.Sprint(dur.Seconds()),
		"--out", out)
	d.Stdout, d.Stderr = os.Stdout, os.Stderr
	if err := d.Start(); err != nil {
		return nil, err
	}
	driver.Store(d)
	return d, nil
}

func waitHealthy(ws []Worker, timeout time.Duration) error {
	c := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(timeout)
	for _, w := range ws {
		for {
			resp, err := c.Get(w.Health)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s not healthy after %s", w.Health, timeout)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	time.Sleep(2 * time.Second) // let the router's own health checks catch up
	return nil
}

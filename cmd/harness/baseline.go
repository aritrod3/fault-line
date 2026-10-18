package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"sort"
	"time"
)

// Baseline is the known-good response per prompt. Prompts that gave more
// than one distinct response under normal load are nondeterministic and
// left out of corruption checks.
type Baseline struct {
	Responses        map[int]string `json:"responses"`
	Nondeterministic []int          `json:"nondeterministic"`
}

func cmdBaseline(args []string) error {
	fs := flag.NewFlagSet("baseline", flag.ExitOnError)
	path := fs.String("scenario", "", "scenario YAML")
	out := fs.String("out", "baseline.json", "baseline path")
	dur := fs.Duration("duration", 20*time.Second, "how long to run the normal load")
	fs.Parse(args)
	sc, _, err := loadScenario(*path)
	if err != nil {
		return err
	}
	os.MkdirAll("run", 0o755)
	if err := waitHealthy(sc.Target.Workers, time.Minute); err != nil {
		return err
	}

	log.Printf("baseline: %s of normal load", *dur)
	d, err := startDriver(sc, *dur, "run/baseline.jsonl")
	if err != nil {
		return err
	}
	if err := d.Wait(); err != nil {
		return err
	}
	recs, err := readRecords("run/baseline.jsonl", 0)
	if err != nil {
		return err
	}

	seen := map[int]map[string]bool{}
	for _, r := range recs {
		if !r.OK {
			continue
		}
		if seen[r.Prompt] == nil {
			seen[r.Prompt] = map[string]bool{}
		}
		seen[r.Prompt][r.Text] = true
	}
	b := Baseline{Responses: map[int]string{}, Nondeterministic: []int{}}
	for p, texts := range seen {
		if len(texts) > 1 {
			b.Nondeterministic = append(b.Nondeterministic, p)
			continue
		}
		for t := range texts {
			b.Responses[p] = t
		}
	}
	sort.Ints(b.Nondeterministic)

	data, _ := json.MarshalIndent(b, "", "  ")
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	log.Printf("wrote %s: %d prompts deterministic, %d not", *out, len(b.Responses), len(b.Nondeterministic))
	return nil
}

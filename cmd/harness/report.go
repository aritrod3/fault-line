package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

type Report struct {
	Stack    string    `json:"stack"`
	Version  string    `json:"version"`
	Model    string    `json:"model"`
	Host     Host      `json:"host"`
	Started  time.Time `json:"started"`
	Scenario string    `json:"scenario"` // the YAML exactly as run
	Baseline *Baseline `json:"baseline,omitempty"`
	Results  []Result  `json:"results"`
}

type Host struct {
	Name string `json:"name"`
	OS   string `json:"os"`
	Arch string `json:"arch"`
	CPUs int    `json:"cpus"`
	GPU  string `json:"gpu"`
	Go   string `json:"go"`
}

type Result struct {
	Fault     string   `json:"fault"`
	Target    string   `json:"target"`
	AtS       float64  `json:"at_s"`
	DurationS float64  `json:"duration_s"`
	Netem     string   `json:"netem,omitempty"`
	Error     string   `json:"error,omitempty"`
	Metrics   *Metrics `json:"metrics,omitempty"`
}

func cmdReport(args []string) error {
	if len(args) < 1 {
		usage()
	}
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	format := fs.String("format", "md", "output format (md)")
	fs.Parse(args[1:])
	if *format != "md" {
		return fmt.Errorf("unsupported format %q", *format)
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}

	fmt.Printf("## %s %s · model %s\n\n", r.Stack, r.Version, r.Model)
	fmt.Printf("%s, %s/%s, %d CPUs, GPU %s, %s · %s\n\n", r.Host.Name, r.Host.OS, r.Host.Arch, r.Host.CPUs, r.Host.GPU, r.Host.Go, r.Started.Format(time.RFC3339))
	if b := r.Baseline; b != nil {
		fmt.Printf("Baseline: %d prompts deterministic, %d not.\n\n", len(b.Responses), len(b.Nondeterministic))
	} else {
		fmt.Print("No baseline: corruption not measured.\n\n")
	}
	fmt.Println("| Fault | Target | Detection | Recovery | Lost | Corrupted | Throughput | p99 |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, res := range r.Results {
		if m := res.Metrics; m != nil {
			corrupted := "–"
			if m.Corrupted != nil {
				corrupted = fmt.Sprint(*m.Corrupted)
			}
			fmt.Printf("| %s | %s | %s | %s | %d | %s | %.2f× | %.1f× |\n",
				res.Fault, res.Target, secs(m.DetectionS, "none"), secs(m.RecoveryS, "not recovered"),
				m.Lost, corrupted, m.ThroughputFrac, m.P99Frac)
		} else {
			fmt.Printf("| %s | %s | error: %s | | | | | |\n", res.Fault, res.Target, res.Error)
		}
	}
	return nil
}

func secs(v *float64, none string) string {
	if v == nil {
		return none
	}
	return fmt.Sprintf("%.1fs", *v)
}

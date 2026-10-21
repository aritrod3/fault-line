package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Scenario struct {
	Stack   string `yaml:"stack"`
	Version string `yaml:"version"`
	Model   string `yaml:"model"`
	Target  struct {
		Router  string   `yaml:"router"`
		Workers []Worker `yaml:"workers"`
	} `yaml:"target"`
	Load struct {
		Prompts     string        `yaml:"prompts"`
		Concurrency int           `yaml:"concurrency"`
		Duration    time.Duration `yaml:"duration"`
	} `yaml:"load"`
	Faults []Fault `yaml:"faults"`
}

type Worker struct {
	Start          []string `yaml:"start"`
	PIDFile        string   `yaml:"pid_file"`
	Health         string   `yaml:"health"`
	Iface          string   `yaml:"iface"`
	Port           int      `yaml:"port"`             // if set, slow_network only delays this port
	CorruptPIDFile string   `yaml:"corrupt_pid_file"` // corrupter in front of this worker
	SlowfsPIDFile  string   `yaml:"slowfs_pid_file"`  // slowfs serving its weights
	Cgroup         string   `yaml:"cgroup"`           // cgroup v2 directory it runs in
}

type Fault struct {
	Type       string        `yaml:"type"`
	Target     string        `yaml:"target"`
	At         time.Duration `yaml:"at"`
	Duration   time.Duration `yaml:"duration"`
	Netem      string        `yaml:"netem"`
	MemoryFrac float64       `yaml:"memory_frac"` // memory.max as a fraction of current usage
}

func loadScenario(path string) (*Scenario, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var sc Scenario
	if err := yaml.Unmarshal(raw, &sc); err != nil {
		return nil, nil, err
	}
	for i := range sc.Faults {
		f := &sc.Faults[i]
		if f.Type == "memory_pressure" && f.MemoryFrac == 0 {
			f.MemoryFrac = 0.8
		}
		switch f.Type {
		case "kill_backend", "hang_backend", "slow_network", "corrupt_output", "storage_stall", "memory_pressure":
		default:
			return nil, nil, fmt.Errorf("unknown fault type %q", f.Type)
		}
		w, err := sc.worker(f.Target)
		if err != nil {
			return nil, nil, err
		}
		if f.Type == "corrupt_output" && w.CorruptPIDFile == "" ||
			f.Type == "storage_stall" && w.SlowfsPIDFile == "" ||
			f.Type == "memory_pressure" && w.Cgroup == "" {
			return nil, nil, fmt.Errorf("%s: %s is missing the field this fault needs", f.Type, f.Target)
		}
		if f.At < 2*baselineFrom*time.Second {
			return nil, nil, fmt.Errorf("%s: at must be >= %ds to leave room for a baseline", f.Type, 2*baselineFrom)
		}
		if f.At+f.Duration+(sustainSecs+smoothSecs)*time.Second > sc.Load.Duration {
			return nil, nil, fmt.Errorf("%s: load duration too short to observe recovery", f.Type)
		}
	}
	return &sc, raw, nil
}

func (sc *Scenario) worker(target string) (Worker, error) {
	var i int
	if _, err := fmt.Sscanf(target, "workers[%d]", &i); err != nil || i < 0 || i >= len(sc.Target.Workers) {
		return Worker{}, fmt.Errorf("bad target %q", target)
	}
	return sc.Target.Workers[i], nil
}

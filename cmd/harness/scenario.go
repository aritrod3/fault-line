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
	Start   []string `yaml:"start"`
	PIDFile string   `yaml:"pid_file"`
	Health  string   `yaml:"health"`
	Iface   string   `yaml:"iface"`
	Port    int      `yaml:"port"` // if set, slow_network only delays this port
	// PID of a corrupter in front of this worker, if any.
	CorruptPIDFile string `yaml:"corrupt_pid_file"`
}

type Fault struct {
	Type     string        `yaml:"type"`
	Target   string        `yaml:"target"`
	At       time.Duration `yaml:"at"`
	Duration time.Duration `yaml:"duration"`
	Netem    string        `yaml:"netem"`
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
	for _, f := range sc.Faults {
		switch f.Type {
		case "kill_backend", "hang_backend", "slow_network", "corrupt_output":
		default:
			return nil, nil, fmt.Errorf("unknown fault type %q", f.Type)
		}
		w, err := sc.worker(f.Target)
		if err != nil {
			return nil, nil, err
		}
		if f.Type == "corrupt_output" && w.CorruptPIDFile == "" {
			return nil, nil, fmt.Errorf("corrupt_output: %s has no corrupt_pid_file", f.Target)
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

package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const journalFile = "run/fault.journal"

// journal holds the undo command for the active fault. It is written before
// the fault is applied, so a crashed run can be reversed by the next one.
type journal struct {
	Fault  string   `json:"fault"`
	Undo   []string `json:"undo"`
	Detach bool     `json:"detach"` // undo starts a long-lived process
}

func applyFault(f Fault, w Worker) error {
	var apply [][]string
	j := journal{Fault: f.Type + " " + f.Target}
	switch f.Type {
	case "kill_backend", "hang_backend":
		p, err := signalTarget(w.PIDFile)
		if err != nil {
			return err
		}
		if f.Type == "kill_backend" {
			apply = [][]string{{"kill", "-KILL", "--", p}}
			j.Undo, j.Detach = w.Start, true
		} else {
			apply = [][]string{{"kill", "-STOP", "--", p}}
			j.Undo = []string{"kill", "-CONT", "--", p}
		}
	case "corrupt_output":
		pid, err := os.ReadFile(w.CorruptPIDFile)
		if err != nil {
			return err
		}
		p := strings.TrimSpace(string(pid))
		apply = [][]string{{"kill", "-USR1", p}}
		j.Undo = []string{"kill", "-USR2", p}
	case "slow_network":
		dev, netem := w.Iface, strings.Fields(f.Netem)
		if w.Port == 0 {
			apply = [][]string{tc(append([]string{"qdisc", "add", "dev", dev, "root", "netem"}, netem...)...)}
		} else {
			// Only traffic from the worker's port goes to the netem band.
			apply = [][]string{
				tc("qdisc", "add", "dev", dev, "root", "handle", "1:", "prio"),
				tc(append([]string{"qdisc", "add", "dev", dev, "parent", "1:3", "handle", "30:", "netem"}, netem...)...),
				tc("filter", "add", "dev", dev, "parent", "1:0", "protocol", "ip", "u32",
					"match", "ip", "sport", strconv.Itoa(w.Port), "0xffff", "flowid", "1:3"),
			}
		}
		j.Undo = tc("qdisc", "del", "dev", dev, "root")
	}
	data, _ := json.Marshal(j)
	if err := os.WriteFile(journalFile, data, 0o644); err != nil {
		return err
	}
	for i, cmd := range apply {
		if out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput(); err != nil {
			if i == 0 {
				os.Remove(journalFile) // nothing applied
			} else {
				reverse()
			}
			return errors.New(strings.TrimSpace(string(out)) + ": " + err.Error())
		}
	}
	return nil
}

// signalTarget returns the PID to signal, or -PID for the whole process
// group when the worker leads one (vLLM runs its engine in a child process).
func signalTarget(pidFile string) (string, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return "", err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return "", err
	}
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		return "-" + strconv.Itoa(pid), nil
	}
	return strconv.Itoa(pid), nil
}

func tc(args ...string) []string {
	if os.Geteuid() != 0 {
		return append([]string{"sudo", "-n", "tc"}, args...)
	}
	return append([]string{"tc"}, args...)
}

var reverseMu sync.Mutex

// reverse runs the journaled undo, if any. Safe to call repeatedly.
func reverse() {
	reverseMu.Lock()
	defer reverseMu.Unlock()
	data, err := os.ReadFile(journalFile)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var j journal
	if err == nil {
		err = json.Unmarshal(data, &j)
	}
	if err == nil {
		if j.Detach {
			err = startDetached(j.Undo)
		} else {
			err = exec.Command(j.Undo[0], j.Undo[1:]...).Run()
		}
	}
	if err != nil {
		log.Printf("reverse failed, journal kept: %v", err)
		return
	}
	os.Remove(journalFile)
	log.Printf("reversed %s", j.Fault)
}

// startDetached starts cmd in its own process group so it outlives the harness.
func startDetached(cmd []string) error {
	out, err := os.OpenFile("run/restart.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Stdout, c.Stderr = out, out
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
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
	var apply []string
	j := journal{Fault: f.Type + " " + f.Target}
	switch f.Type {
	case "kill_backend", "hang_backend":
		pid, err := os.ReadFile(w.PIDFile)
		if err != nil {
			return err
		}
		p := strings.TrimSpace(string(pid))
		if f.Type == "kill_backend" {
			apply = []string{"kill", "-KILL", p}
			j.Undo, j.Detach = w.Start, true
		} else {
			apply = []string{"kill", "-STOP", p}
			j.Undo = []string{"kill", "-CONT", p}
		}
	case "corrupt_output":
		pid, err := os.ReadFile(w.CorruptPIDFile)
		if err != nil {
			return err
		}
		p := strings.TrimSpace(string(pid))
		apply = []string{"kill", "-USR1", p}
		j.Undo = []string{"kill", "-USR2", p}
	case "slow_network":
		apply = append([]string{"tc", "qdisc", "add", "dev", w.Iface, "root", "netem"}, strings.Fields(f.Netem)...)
		j.Undo = []string{"tc", "qdisc", "del", "dev", w.Iface, "root"}
	}
	data, _ := json.Marshal(j)
	if err := os.WriteFile(journalFile, data, 0o644); err != nil {
		return err
	}
	if out, err := exec.Command(apply[0], apply[1:]...).CombinedOutput(); err != nil {
		os.Remove(journalFile)
		return errors.New(strings.TrimSpace(string(out)) + ": " + err.Error())
	}
	return nil
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

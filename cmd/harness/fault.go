package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const journalFile = "run/fault.journal"

// journal holds how to undo the active fault. It is written before the
// fault is applied, so a crashed run can be reversed by the next one.
type journal struct {
	Fault   string     `json:"fault"`
	Undo    [][]string `json:"undo"`
	Restart []string   `json:"restart"`  // run if the worker is not running after Undo
	PIDFile string     `json:"pid_file"` // the worker's
}

func applyFault(f Fault, w Worker) error {
	var apply [][]string
	restartAfter := false
	j := journal{Fault: f.Type + " " + f.Target, Restart: w.Start, PIDFile: w.PIDFile}
	switch f.Type {
	case "kill_backend":
		p, err := signalTarget(w.PIDFile)
		if err != nil {
			return err
		}
		apply = [][]string{{"kill", "-KILL", "--", p}}
	case "hang_backend":
		p, err := signalTarget(w.PIDFile)
		if err != nil {
			return err
		}
		apply = [][]string{{"kill", "-STOP", "--", p}}
		j.Undo = [][]string{{"kill", "-CONT", "--", p}}
	case "corrupt_output":
		p, err := readPID(w.CorruptPIDFile)
		if err != nil {
			return err
		}
		apply = [][]string{{"kill", "-USR1", p}}
		j.Undo = [][]string{{"kill", "-USR2", p}}
	case "slow_network":
		dev, netem := w.Iface, strings.Fields(f.Netem)
		if w.Port == 0 {
			apply = [][]string{asRoot(append([]string{"tc", "qdisc", "add", "dev", dev, "root", "netem"}, netem...)...)}
		} else {
			// Only traffic from the worker's port goes to the netem band.
			apply = [][]string{
				asRoot("tc", "qdisc", "add", "dev", dev, "root", "handle", "1:", "prio"),
				asRoot(append([]string{"tc", "qdisc", "add", "dev", dev, "parent", "1:3", "handle", "30:", "netem"}, netem...)...),
				asRoot("tc", "filter", "add", "dev", dev, "parent", "1:0", "protocol", "ip", "u32",
					"match", "ip", "sport", strconv.Itoa(w.Port), "0xffff", "flowid", "1:3"),
			}
		}
		j.Undo = [][]string{asRoot("tc", "qdisc", "del", "dev", dev, "root")}
	case "storage_stall":
		// Slow the weight mount, drop the page cache so reads hit it, and
		// restart the worker so it reloads its weights through it.
		fsPID, err := readPID(w.SlowfsPIDFile)
		if err != nil {
			return err
		}
		p, err := signalTarget(w.PIDFile)
		if err != nil {
			return err
		}
		apply = [][]string{
			{"kill", "-USR1", fsPID},
			asRoot("sh", "-c", "sync && echo 3 > /proc/sys/vm/drop_caches"),
			{"kill", "-KILL", "--", p},
		}
		restartAfter = true
		j.Undo = [][]string{{"kill", "-USR2", fsPID}}
	case "memory_pressure":
		cur, err := os.ReadFile(w.Cgroup + "/memory.current")
		if err != nil {
			return err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(cur)), 10, 64)
		if err != nil {
			return err
		}
		limit := int64(float64(n) * f.MemoryFrac)
		apply = [][]string{asRoot("sh", "-c", fmt.Sprintf("echo %d > %s/memory.max", limit, w.Cgroup))}
		j.Undo = [][]string{asRoot("sh", "-c", fmt.Sprintf("echo max > %s/memory.max", w.Cgroup))}
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
	if restartAfter {
		if err := startDetached(w.Start); err != nil {
			reverse()
			return err
		}
	}
	return nil
}

func readPID(file string) (string, error) {
	data, err := os.ReadFile(file)
	return strings.TrimSpace(string(data)), err
}

// signalTarget returns the PID to signal, or -PID for the whole process
// group when the worker leads one (vLLM runs its engine in a child process).
func signalTarget(pidFile string) (string, error) {
	p, err := readPID(pidFile)
	if err != nil {
		return "", err
	}
	pid, err := strconv.Atoi(p)
	if err != nil {
		return "", err
	}
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		return "-" + p, nil
	}
	return p, nil
}

func running(pidFile string) bool {
	p, err := readPID(pidFile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(p)
	if err != nil {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func asRoot(cmd ...string) []string {
	if os.Geteuid() != 0 {
		return append([]string{"sudo", "-n"}, cmd...)
	}
	return cmd
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
	for _, cmd := range j.Undo {
		if err != nil {
			break
		}
		err = exec.Command(cmd[0], cmd[1:]...).Run()
	}
	if err == nil && len(j.Restart) > 0 && !running(j.PIDFile) {
		err = startDetached(j.Restart)
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
	go c.Wait() // reap it when it exits, so a dead worker doesn't look alive
	return nil
}

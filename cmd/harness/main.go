// harness v0.1: run the load driver, SIGKILL worker0 at killAt, restart it at
// restartAt, then compute recovery time from the driver's request log.
//
// Reversal: before the kill, the restart command is written to a journal.
// The journal is replayed on normal exit, on SIGINT/SIGTERM, and at the start
// of the next run if the harness died without replaying it.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	pidFile     = "run/worker0.pid"
	journalFile = "run/fault.journal"
	requestLog  = "run/requests.jsonl"
	workerLog   = "run/worker0.log"

	duration    = 60 * time.Second
	killAt      = 20 * time.Second
	restartAt   = 30 * time.Second
	baselineLo  = 5  // seconds
	baselineHi  = 20 // seconds, exclusive
	recoveryPct = 0.95
	sustainSecs = 10
	smoothSecs  = 3
)

var restartCmd = []string{"bin/fakeworker", "-port", "8001", "-pidfile", pidFile}

var driverCmd = []string{
	"python3", "driver/driver.py",
	"--url", "http://127.0.0.1:8000/v1/completions",
	"--concurrency", "8",
	"--duration", strconv.Itoa(int(duration.Seconds())),
	"--out", requestLog,
}

type journal struct {
	Fault   string   `json:"fault"`
	Restart []string `json:"restart"`
}

var reverseMu sync.Mutex

// reverse replays the journal if one exists. Safe to call more than once.
func reverse() {
	reverseMu.Lock()
	defer reverseMu.Unlock()
	data, err := os.ReadFile(journalFile)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("reverse: read journal: %v", err)
		return
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		log.Printf("reverse: bad journal: %v", err)
		return
	}
	if err := startDetached(j.Restart); err != nil {
		log.Printf("reverse: restart failed, journal kept: %v", err)
		return
	}
	os.Remove(journalFile)
	log.Printf("reversed %s: ran %s", j.Fault, strings.Join(j.Restart, " "))
}

// startDetached starts cmd in its own process group so it outlives the harness.
func startDetached(cmd []string) error {
	out, err := os.OpenFile(workerLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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

func killWorker() error {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return err
	}
	j, _ := json.Marshal(journal{Fault: "kill_backend worker0", Restart: restartCmd})
	if err := os.WriteFile(journalFile, j, 0o644); err != nil {
		return err // no journal, no fault
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	os.MkdirAll("run", 0o755)

	// A leftover journal means a previous run died mid-fault.
	reverse()
	defer reverse()

	driver := exec.Command(driverCmd[0], driverCmd[1:]...)
	driver.Stdout, driver.Stderr = os.Stdout, os.Stderr

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sigs
		log.Printf("got %v, reversing fault", s)
		reverse()
		if driver.Process != nil {
			driver.Process.Kill()
		}
		os.Exit(1)
	}()

	t0 := time.Now()
	if err := driver.Start(); err != nil {
		log.Printf("start driver: %v", err)
		return
	}
	log.Printf("driver started, %s run", duration)

	time.Sleep(killAt - time.Since(t0))
	killTime := time.Now()
	if err := killWorker(); err != nil {
		log.Printf("kill worker0: %v", err)
		driver.Process.Kill()
		return
	}
	log.Printf("SIGKILL worker0")

	time.Sleep(restartAt - time.Since(t0))
	restartTime := time.Now()
	reverse()

	if err := driver.Wait(); err != nil {
		log.Printf("driver: %v", err)
		return
	}

	if err := analyze(t0, killTime, restartTime); err != nil {
		log.Printf("analyze: %v", err)
	}
}

type record struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	OK    bool    `json:"ok"`
}

func analyze(t0, killTime, restartTime time.Time) error {
	f, err := os.Open(requestLog)
	if err != nil {
		return err
	}
	defer f.Close()

	origin := float64(t0.UnixNano()) / 1e9
	nsec := int(duration.Seconds())
	perSec := make([]int, nsec+1)
	failed := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return err
		}
		if !r.OK {
			failed++
			continue
		}
		s := int(r.End - origin)
		if s >= 0 && s <= nsec {
			perSec[s]++
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}

	base := 0.0
	for s := baselineLo; s < baselineHi; s++ {
		base += float64(perSec[s])
	}
	base /= baselineHi - baselineLo

	restartSec := restartTime.Sub(t0).Seconds()
	recovered := -1.0
	// Per-second counts dip ~15% every few seconds from bucket-boundary
	// quantization, so compare a forward rolling mean, not raw counts.
	smooth := func(k int) float64 {
		sum := 0
		for i := k; i < k+smoothSecs; i++ {
			sum += perSec[i]
		}
		return float64(sum) / smoothSecs
	}
	for s := int(math.Ceil(restartSec)); s+sustainSecs+smoothSecs-1 <= nsec; s++ {
		ok := true
		for k := s; k < s+sustainSecs; k++ {
			if smooth(k) < recoveryPct*base {
				ok = false
				break
			}
		}
		if ok {
			recovered = float64(s) - restartSec
			break
		}
	}

	fmt.Println()
	fmt.Println("throughput per second (ok requests):")
	for s, n := range perSec[:nsec] {
		mark := ""
		switch s {
		case int(killTime.Sub(t0).Seconds()):
			mark = "  <- kill"
		case int(restartSec):
			mark = "  <- restart"
		}
		fmt.Printf("  %3ds %4d%s\n", s, n, mark)
	}
	fmt.Println()
	fmt.Printf("baseline throughput: %.1f req/s\n", base)
	fmt.Printf("requests failed:     %d\n", failed)
	if recovered < 0 {
		fmt.Printf("recovery time:       not recovered within run\n")
	} else {
		fmt.Printf("recovery time:       %.1fs (restart to %d%% baseline, %ds rolling mean, sustained %ds)\n",
			recovered, int(recoveryPct*100), smoothSecs, sustainSecs)
	}
	return nil
}

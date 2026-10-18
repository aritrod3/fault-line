package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"
)

const (
	baselineFrom = 5 // seconds; skip warm-up
	recoveryPct  = 0.95
	sustainSecs  = 10
	smoothSecs   = 3 // per-second counts are noisy, compare a rolling mean
)

type Metrics struct {
	BaselineRPS    float64  `json:"baseline_rps"`
	BaselineP99S   float64  `json:"baseline_p99_s"`
	DetectionS     *float64 `json:"detection_s"` // nil: no reaction seen
	RecoveryS      *float64 `json:"recovery_s"`  // nil: not recovered within run
	Lost           int      `json:"lost"`
	Corrupted      *int     `json:"corrupted"` // nil: no baseline
	ThroughputFrac float64  `json:"throughput_frac"`
	P99Frac        float64  `json:"p99_frac"`
}

type record struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	OK     bool    `json:"ok"`
	Prompt int     `json:"prompt"`
	Text   string  `json:"text"`
}

// readRecords reads a driver log, with times relative to origin (unix seconds).
func readRecords(path string, origin float64) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var recs []record
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		r.Start -= origin
		r.End -= origin
		recs = append(recs, r)
	}
	return recs, sc.Err()
}

// analyze computes metrics for one fault window. ref maps prompt index to
// its baseline response; nil means corruption is not measured.
func analyze(path string, t0, inject, clear time.Time, dur time.Duration, ref map[int]string) (*Metrics, error) {
	recs, err := readRecords(path, unix(t0))
	if err != nil {
		return nil, err
	}
	in, cl := inject.Sub(t0).Seconds(), clear.Sub(t0).Seconds()
	n := int(dur.Seconds())

	perSec := make([]float64, n+1)
	var baseLat, faultLat []float64
	for _, r := range recs {
		if r.OK && r.End >= 0 && int(r.End) <= n {
			perSec[int(r.End)]++
		}
		if r.OK && r.Start >= baselineFrom && r.End < in {
			baseLat = append(baseLat, r.End-r.Start)
		}
		if r.Start >= in && r.Start < cl {
			faultLat = append(faultLat, r.End-r.Start)
		}
	}
	m := &Metrics{
		BaselineRPS:  mean(perSec[baselineFrom:int(in)]),
		BaselineP99S: p99(baseLat),
	}
	m.ThroughputFrac = mean(perSec[int(math.Ceil(in)):int(cl)]) / m.BaselineRPS
	m.P99Frac = p99(faultLat) / m.BaselineP99S

	// First reaction: a failure, seen when it returns, or a request slower
	// than 2x baseline p99, seen once it crosses that line.
	slow := 2 * m.BaselineP99S
	det := math.Inf(1)
	for _, r := range recs {
		if r.End < in {
			continue
		}
		t := math.Inf(1)
		if !r.OK {
			m.Lost++
			t = r.End
		} else if r.End-r.Start > slow {
			t = r.Start + slow
		}
		if t >= in {
			det = math.Min(det, t)
		}
	}
	if !math.IsInf(det, 1) {
		m.DetectionS = ptr(det - in)
	}

	if ref != nil {
		bad := 0
		for _, r := range recs {
			if want, ok := ref[r.Prompt]; ok && r.OK && r.End >= in && r.Text != want {
				bad++
			}
		}
		m.Corrupted = &bad
	}

	for s := int(math.Ceil(cl)); s+sustainSecs+smoothSecs-1 <= n; s++ {
		ok := true
		for k := s; k < s+sustainSecs; k++ {
			if mean(perSec[k:k+smoothSecs]) < recoveryPct*m.BaselineRPS {
				ok = false
				break
			}
		}
		if ok {
			m.RecoveryS = ptr(float64(s) - cl)
			break
		}
	}
	return m, nil
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func ptr(f float64) *float64 { return &f }

func mean(xs []float64) float64 {
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func p99(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[int(math.Ceil(0.99*float64(len(s))))-1]
}

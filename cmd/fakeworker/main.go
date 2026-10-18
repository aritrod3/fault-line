// fakeworker stands in for a vLLM worker. It sleeps -load-delay before
// listening (model load), then serves /v1/completions with a fixed latency
// and a fixed number of concurrent slots (batch capacity).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	port := flag.Int("port", 8001, "listen port")
	pidFile := flag.String("pidfile", "", "write PID here")
	loadDelay := flag.Duration("load-delay", 5*time.Second, "simulated model load time")
	latency := flag.Duration("latency", 200*time.Millisecond, "per-request latency")
	slots := flag.Int("slots", 4, "concurrent requests served")
	flag.Parse()

	if *pidFile != "" {
		if err := os.WriteFile(*pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			log.Fatal(err)
		}
	}

	log.Printf("loading model (%s)", *loadDelay)
	time.Sleep(*loadDelay)

	sem := make(chan struct{}, *slots)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	http.HandleFunc("/v1/completions", func(w http.ResponseWriter, r *http.Request) {
		sem <- struct{}{}
		defer func() { <-sem }()
		time.Sleep(*latency)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"text":"ok from :%d"}]}`, *port)
	})

	log.Printf("listening on :%d", *port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), nil))
}

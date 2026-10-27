// proxy is the round-robin router under test. It skips backends that are
// marked down, retries a request on the next backend if the connection is
// refused, and re-checks every backend's health path once a second.
package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type backend struct {
	url string
	up  atomic.Bool
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8000", "listen address")
	backends := flag.String("backends", "http://127.0.0.1:8001,http://127.0.0.1:8002", "comma-separated backend URLs")
	health := flag.String("health", "/health", "liveness path checked on each backend")
	flag.Parse()

	var bs []*backend
	for _, u := range strings.Split(*backends, ",") {
		b := &backend{url: u}
		b.up.Store(true)
		bs = append(bs, b)
	}

	go healthLoop(bs, *health)

	client := &http.Client{Timeout: 30 * time.Second}
	var mu sync.Mutex
	next := 0

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		start := next
		next = (next + 1) % len(bs)
		mu.Unlock()

		for i := 0; i < len(bs); i++ {
			b := bs[(start+i)%len(bs)]
			if !b.up.Load() {
				continue
			}
			req, _ := http.NewRequest(r.Method, b.url+r.URL.Path, bytes.NewReader(body))
			req.Header = r.Header.Clone()
			resp, err := client.Do(req)
			if err != nil {
				if isDialError(err) {
					// Nothing was sent; safe to mark down and try the next one.
					b.up.Store(false)
					log.Printf("%s down: %v", b.url, err)
					continue
				}
				// Failed mid-request: the request may have been processed, don't retry.
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			resp.Body.Close()
			return
		}
		http.Error(w, "no backend available", http.StatusServiceUnavailable)
	})

	log.Printf("proxy on %s -> %s", *listen, *backends)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

func healthLoop(bs []*backend, path string) {
	c := &http.Client{Timeout: 500 * time.Millisecond}
	for range time.Tick(time.Second) {
		for _, b := range bs {
			resp, err := c.Get(b.url + path)
			ok := err == nil && resp.StatusCode == http.StatusOK
			if resp != nil {
				resp.Body.Close()
			}
			if ok != b.up.Load() {
				log.Printf("%s up=%v", b.url, ok)
			}
			b.up.Store(ok)
		}
	}
}

func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

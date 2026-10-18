// corrupter is a pass-through proxy in front of one worker. SIGUSR1 turns
// corruption on (one flipped bit in each completion's text); SIGUSR2 turns it off.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8012", "listen address")
	upstream := flag.String("upstream", "http://127.0.0.1:8002", "worker URL")
	pidFile := flag.String("pidfile", "", "write PID here")
	flag.Parse()

	if *pidFile != "" {
		if err := os.WriteFile(*pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			log.Fatal(err)
		}
	}

	var on atomic.Bool
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for s := range sigs {
			on.Store(s == syscall.SIGUSR1)
			log.Printf("corrupt=%v", on.Load())
		}
	}()

	u, err := url.Parse(*upstream)
	if err != nil {
		log.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.ModifyResponse = func(resp *http.Response) error {
		if !on.Load() || resp.Request.URL.Path != "/v1/completions" {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		body = corrupt(body)
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		return nil
	}

	log.Printf("corrupter on %s -> %s", *listen, *upstream)
	log.Fatal(http.ListenAndServe(*listen, rp))
}

// corrupt flips one bit in the middle of each choice's text. Bodies it
// can't parse pass through unchanged.
func corrupt(body []byte) []byte {
	var v map[string]any
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	choices, _ := v["choices"].([]any)
	for _, c := range choices {
		m, _ := c.(map[string]any)
		if s, ok := m["text"].(string); ok && s != "" {
			b := []byte(s)
			b[len(b)/2] ^= 1
			m["text"] = string(b)
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

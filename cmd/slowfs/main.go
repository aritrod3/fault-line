// slowfs mounts -src at -mnt as a FUSE passthrough. SIGUSR1 adds -delay to
// every read; SIGUSR2 removes it. SIGINT/SIGTERM unmount.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

var slow atomic.Bool

func main() {
	src := flag.String("src", "", "directory to pass through")
	mnt := flag.String("mnt", "", "mount point")
	delay := flag.Duration("delay", 10*time.Millisecond, "added to each read while slow")
	pidFile := flag.String("pidfile", "", "write PID here")
	flag.Parse()

	root, err := fs.NewLoopbackRoot(*src)
	if err != nil {
		log.Fatal(err)
	}
	os.MkdirAll(*mnt, 0o755)
	server, err := fs.Mount(*mnt, &node{root.(*fs.LoopbackNode), *delay}, &fs.Options{
		MountOptions: fuse.MountOptions{FsName: "slowfs"},
	})
	if err != nil {
		log.Fatal(err)
	}
	if *pidFile != "" {
		os.WriteFile(*pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for s := range sigs {
			if s == syscall.SIGUSR1 || s == syscall.SIGUSR2 {
				slow.Store(s == syscall.SIGUSR1)
				log.Printf("slow=%v", slow.Load())
				continue
			}
			server.Unmount()
		}
	}()
	log.Printf("slowfs %s -> %s", *src, *mnt)
	server.Wait()
}

type node struct {
	*fs.LoopbackNode
	delay time.Duration
}

func (n *node) WrapChild(ctx context.Context, ops fs.InodeEmbedder) fs.InodeEmbedder {
	return &node{ops.(*fs.LoopbackNode), n.delay}
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	fh, fl, errno := n.LoopbackNode.Open(ctx, flags)
	if errno != 0 {
		return nil, 0, errno
	}
	return &file{fh.(*fs.LoopbackFile), n.delay}, fl, 0
}

type file struct {
	*fs.LoopbackFile
	delay time.Duration
}

func (f *file) Read(ctx context.Context, buf []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if slow.Load() {
		time.Sleep(f.delay)
	}
	return f.LoopbackFile.Read(ctx, buf, off)
}

// PassthroughFd is disabled so reads can't bypass the delay.
func (f *file) PassthroughFd() (int, bool) { return 0, false }

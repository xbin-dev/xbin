// Command latency is a TCP proxy that delays every byte by a fixed time in
// each direction, so a browser on this machine sees xbind as if it were far
// away (hack/demo/measurements.md, "Predictive echo"). A round trip through
// it gains twice -delay. Order and bandwidth are kept: each chunk read is
// written -delay after it was read, in the order it was read.
//
//	go run ./hack/demo/latency -listen 127.0.0.1:9343 -to 127.0.0.1:9341 -delay 150ms
//
// Then open http://127.0.0.1:9343/ instead of xbind's own address.
package main

import (
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9343", "address to listen on")
	to := flag.String("to", "127.0.0.1:9341", "address to forward to")
	delay := flag.Duration("delay", 150*time.Millisecond, "delay added in each direction (the round trip gains twice it)")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("latency: %s → %s, +%s each way (+%s round trip)", ln.Addr(), *to, *delay, 2**delay)
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		ln.Close()
	}()
	var conns atomic.Int64
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.Printf("latency: stopped after %d connections", conns.Load())
				return
			}
			log.Print(err)
			continue
		}
		conns.Add(1)
		go serve(c, *to, *delay)
	}
}

func serve(c net.Conn, to string, delay time.Duration) {
	up, err := net.Dial("tcp", to)
	if err != nil {
		log.Printf("latency: dial %s: %v", to, err)
		c.Close()
		return
	}
	for _, x := range []net.Conn{c, up} {
		if tc, ok := x.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
	}
	done := make(chan struct{}, 2)
	go func() { pipe(up, c, delay); done <- struct{}{} }()
	go func() { pipe(c, up, delay); done <- struct{}{} }()
	<-done
	<-done
	c.Close()
	up.Close()
}

type chunk struct {
	b   []byte
	due time.Time
}

// pipe copies src to dst, each chunk delay after it was read; at src's EOF
// it half-closes dst once the chunks before it are out.
func pipe(dst, src net.Conn, delay time.Duration) {
	q := make(chan chunk, 4096)
	go func() {
		defer close(q)
		for {
			buf := make([]byte, 32<<10)
			n, err := src.Read(buf)
			if n > 0 {
				q <- chunk{buf[:n], time.Now().Add(delay)}
			}
			if err != nil {
				if err != io.EOF && !errors.Is(err, net.ErrClosed) {
					log.Printf("latency: read: %v", err)
				}
				return
			}
		}
	}()
	for ch := range q {
		time.Sleep(time.Until(ch.due))
		if _, err := dst.Write(ch.b); err != nil {
			src.Close() // the reader ends; drain what it queued
			for range q {
			}
			return
		}
	}
	if tc, ok := dst.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

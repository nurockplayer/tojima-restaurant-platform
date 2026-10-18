// Command demogateway is the loopback-only entrance to the disposable demo.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/demogateway"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:8090", "literal loopback listen address")
	upstream := flag.String("upstream", "http://127.0.0.1:8082", "literal loopback http upstream origin")
	public := flag.String("public-origin", os.Getenv("TOJIMA_DEMO_PUBLIC_ORIGIN"), "exact public https origin")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	cfg := demogateway.Config{Listen: *listen, Upstream: *upstream, PublicOrigin: *public, Secret: os.Getenv("TOJIMA_DEMO_GATE_SECRET")}
	handler, err := demogateway.New(cfg)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return errors.New("cannot listen on demo gateway loopback address")
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Fprintln(os.Stdout, "demo gateway listening on loopback")
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("demo gateway shutdown failed")
		}
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("demo gateway stopped unexpectedly")
	}
}

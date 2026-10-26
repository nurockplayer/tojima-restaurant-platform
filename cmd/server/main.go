package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/config"
)

func main() {
	log.SetFlags(0)
	log.SetOutput(jstLogWriter{})
	healthcheck := flag.Bool("healthcheck", false, "request APP_ADDR/healthz once and exit; used by container healthchecks")
	flag.Parse()
	if err := run(*healthcheck); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// Prefix each line with its actual JST instant, including worker/server logs.
type jstLogWriter struct{}

func (jstLogWriter) Write(message []byte) (int, error) {
	_, err := fmt.Fprintf(os.Stderr, "%s %s", time.Now().In(time.FixedZone("JST", 9*60*60)).Format("2006-01-02 15:04:05 MST"), message)
	if err != nil {
		return 0, err
	}
	return len(message), nil
}

func run(healthcheck bool) error {
	if healthcheck {
		address, err := config.LoadAddressFromEnv(os.LookupEnv)
		if err != nil {
			return err
		}
		if err := probeHealth(address); err != nil {
			return errors.New("healthcheck failed")
		}
		return nil
	}
	cfg, err := config.LoadFromEnv(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runtime, err := composeRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	defer runtime.close()
	server := &http.Server{
		Addr: cfg.HTTPAddress, Handler: runtime.handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddress)
	if err != nil {
		return errors.New("cannot listen on APP_ADDR")
	}
	if runtime.payments != nil {
		defer startPaymentWorker(ctx, runtime.payments.ProcessNext)()
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			<-done
			return errors.New("HTTP graceful shutdown timed out")
		}
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server stopped unexpectedly")
		}
		return nil
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server stopped unexpectedly")
		}
		return nil
	}
}

// probeHealth requests /healthz on the configured listener address. The
// container image is distroless (no shell or curl), so compose runs the server
// binary itself in this mode. Wildcard binds are probed over loopback.
func probeHealth(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/healthz"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return nil
}

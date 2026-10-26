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
	"github.com/nurockplayer/tojima-restaurant-platform/internal/httpapi"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "request APP_ADDR/healthz once and exit; used by container healthchecks")
	flag.Parse()

	cfg, err := config.LoadFromEnv(os.LookupEnv)
	if err != nil {
		log.Fatalf("load configuration: %v", err)
	}
	if *healthcheck {
		if err := probeHealth(cfg.HTTPAddress); err != nil {
			log.Fatalf("healthcheck: %v", err)
		}
		return
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           httpapi.NewHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve HTTP: %v", err)
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

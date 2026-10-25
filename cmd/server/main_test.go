package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeHealthAcceptsOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := probeHealth(strings.TrimPrefix(server.URL, "http://")); err != nil {
		t.Fatalf("probeHealth() error = %v", err)
	}
}

func TestProbeHealthRejectsErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if err := probeHealth(strings.TrimPrefix(server.URL, "http://")); err == nil {
		t.Fatal("probeHealth() error = nil, want non-200 status error")
	}
}

func TestProbeHealthMapsWildcardHostToLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if err := probeHealth(net.JoinHostPort("0.0.0.0", port)); err != nil {
		t.Fatalf("probeHealth() error = %v", err)
	}
}

func TestProbeHealthRejectsMalformedAddress(t *testing.T) {
	if err := probeHealth("localhost"); err == nil {
		t.Fatal("probeHealth() error = nil, want malformed address error")
	}
}

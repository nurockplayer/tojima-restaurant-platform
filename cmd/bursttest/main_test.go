package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// limitedServer allows the first allow requests and returns 429 after that.
func limitedServer(t *testing.T, allow int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	seen := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen++
		n := seen
		mu.Unlock()
		if n > allow {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// slowServer answers 200 only after delay, to push later requests past the
// burst window.
func slowServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runBurst(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestPassWhenIngressLimitsAtExpectedNumber(t *testing.T) {
	srv := limitedServer(t, 5)
	code, stdout, stderr := runBurst(t, "-base-url", srv.URL, "-profile", "v1",
		"-requests", "8", "-expect-limit", "5", "-window", "100ms", "-concurrency", "1")
	if code != exitPass {
		t.Fatalf("exit = %d, want %d; stdout=%s stderr=%s", code, exitPass, stdout, stderr)
	}
	for _, want := range []string{"result: PASS", "status 429: 3", "first 429: index 5"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestFailWhenIngressNeverLimits(t *testing.T) {
	srv := limitedServer(t, 1000)
	code, stdout, stderr := runBurst(t, "-base-url", srv.URL, "-profile", "v1",
		"-requests", "8", "-expect-limit", "5", "-window", "100ms", "-concurrency", "1")
	if code != exitFail {
		t.Fatalf("exit = %d, want %d; stdout=%s stderr=%s", code, exitFail, stdout, stderr)
	}
	for _, want := range []string{"result: FAIL", "first 429: none"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestFailWhenIngressLimitsTooEarly(t *testing.T) {
	srv := limitedServer(t, 3)
	code, stdout, stderr := runBurst(t, "-base-url", srv.URL, "-profile", "v1",
		"-requests", "8", "-expect-limit", "5", "-window", "100ms", "-concurrency", "1")
	if code != exitFail {
		t.Fatalf("exit = %d, want %d; stdout=%s stderr=%s", code, exitFail, stdout, stderr)
	}
	if !strings.Contains(stdout, "result: FAIL") || !strings.Contains(stdout, "first 429: index 3") ||
		!strings.Contains(stdout, "limit tighter than expected") {
		t.Fatalf("unexpected stdout:\n%s", stdout)
	}
}

func TestFailWhenIngressLimitsLaterThanExpected(t *testing.T) {
	srv := limitedServer(t, 8)
	code, stdout, stderr := runBurst(t, "-base-url", srv.URL, "-profile", "v1",
		"-requests", "14", "-expect-limit", "5", "-window", "100ms", "-concurrency", "1")
	if code != exitFail {
		t.Fatalf("exit = %d, want %d; stdout=%s stderr=%s", code, exitFail, stdout, stderr)
	}
	if !strings.Contains(stdout, "result: FAIL") || !strings.Contains(stdout, "first 429: index 8") ||
		!strings.Contains(stdout, "limit looser than expected") {
		t.Fatalf("unexpected stdout:\n%s", stdout)
	}
}

func TestInvalidWhenDispatchOverrunsWindow(t *testing.T) {
	srv := slowServer(t, 1500*time.Millisecond)
	code, stdout, stderr := runBurst(t, "-base-url", srv.URL, "-profile", "v1",
		"-requests", "2", "-expect-limit", "1", "-window", "100ms", "-concurrency", "1")
	if code != exitFail {
		t.Fatalf("exit = %d, want %d; stdout=%s stderr=%s", code, exitFail, stdout, stderr)
	}
	if !strings.Contains(stdout, "result: INVALID (dispatch overran window") {
		t.Fatalf("unexpected stdout:\n%s", stdout)
	}
}

func TestCommitProfileUsesDefaultLimit(t *testing.T) {
	srv := limitedServer(t, 10)
	code, stdout, stderr := runBurst(t, "-base-url", srv.URL, "-profile", "commit",
		"-requests", "12", "-window", "120ms", "-concurrency", "1")
	if code != exitPass {
		t.Fatalf("exit = %d, want %d; stdout=%s stderr=%s", code, exitPass, stdout, stderr)
	}
	if !strings.Contains(stdout, "expect-limit=10") || !strings.Contains(stdout, "first 429: index 10") {
		t.Fatalf("unexpected stdout:\n%s", stdout)
	}
}

func TestRefusesURLWithUserinfo(t *testing.T) {
	for _, base := range []string{"http://user:pass@127.0.0.1:8080", "http://127.0.0.1:8080/?token=x", "http://127.0.0.1:8080/#frag"} {
		code, _, stderr := runBurst(t, "-base-url", base, "-profile", "v1")
		if code != exitUsage {
			t.Fatalf("%s: exit = %d, want %d", base, code, exitUsage)
		}
		if !strings.Contains(stderr, "must not contain userinfo, a query or a fragment") {
			t.Fatalf("%s: stderr missing refusal:\n%s", base, stderr)
		}
	}
}

func TestRefusesNonLoopbackWithoutAllowRemote(t *testing.T) {
	code, _, stderr := runBurst(t, "-base-url", "https://ingress.example.com", "-profile", "v1")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "-allow-remote") {
		t.Fatalf("stderr missing refusal:\n%s", stderr)
	}
}

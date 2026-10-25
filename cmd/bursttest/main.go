// bursttest checks that a deployed ingress enforces the D8 pilot rate limits.
// It is deployment-agnostic: it sends no cookies, auth headers or secrets, and
// refuses non-loopback hosts unless -allow-remote is given. It does not throttle
// the application itself.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	userAgent        = "tojima-bursttest/1"
	defaultSessionID = "00000000-0000-0000-0000-000000000000"

	exitPass  = 0
	exitFail  = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// profile is one rate-limited ingress path and the D8 limit that applies to it.
type profile struct {
	name   string
	method string
	path   string
	limit  int
}

func profileFor(name, sessionID string) (profile, error) {
	base := "/v1/guest/sessions/" + url.PathEscape(sessionID)
	switch name {
	case "v1":
		return profile{name: name, method: http.MethodGet, path: base + "/menu", limit: 120}, nil
	case "commit":
		return profile{name: name, method: http.MethodPost, path: base + "/cart/commit", limit: 10}, nil
	case "checkout":
		return profile{name: name, method: http.MethodPost, path: base + "/checkout-context", limit: 10}, nil
	}
	return profile{}, fmt.Errorf("unknown -profile %q (want v1, commit or checkout)", name)
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bursttest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	baseURL := fs.String("base-url", "", "ingress base URL, e.g. http://127.0.0.1:8080 (required)")
	profileName := fs.String("profile", "v1", "profile to probe: v1, commit or checkout")
	sessionID := fs.String("session-id", defaultSessionID, "synthetic session ID used in the path")
	requests := fs.Int("requests", 0, "total requests to send (default -expect-limit + 20)")
	window := fs.Duration("window", time.Minute, "time window for the whole burst")
	concurrency := fs.Int("concurrency", 4, "number of concurrent senders")
	expectLimit := fs.Int("expect-limit", 0, "requests allowed before 429 (default 120 for v1, 10 for commit and checkout)")
	allowRemote := fs.Bool("allow-remote", false, "allow a non-loopback host")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if *baseURL == "" {
		fmt.Fprintln(stderr, "bursttest: -base-url is required")
		return exitUsage
	}
	base, err := url.Parse(*baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		fmt.Fprintf(stderr, "bursttest: -base-url must be an http(s) URL with a host, got %q\n", *baseURL)
		return exitUsage
	}
	if !isLoopback(base) && !*allowRemote {
		fmt.Fprintf(stderr, "bursttest: refusing to send a burst to non-loopback host %q without -allow-remote\n", base.Host)
		return exitUsage
	}
	spec, err := profileFor(*profileName, *sessionID)
	if err != nil {
		fmt.Fprintf(stderr, "bursttest: %v\n", err)
		return exitUsage
	}
	if *expectLimit == 0 {
		*expectLimit = spec.limit
	}
	if *requests == 0 {
		*requests = *expectLimit + 20
	}
	if *expectLimit <= 0 {
		fmt.Fprintln(stderr, "bursttest: -expect-limit must be positive")
		return exitUsage
	}
	if *requests <= *expectLimit {
		fmt.Fprintf(stderr, "bursttest: -requests (%d) must exceed -expect-limit (%d) to observe a 429\n", *requests, *expectLimit)
		return exitUsage
	}
	if *window <= 0 {
		fmt.Fprintln(stderr, "bursttest: -window must be positive")
		return exitUsage
	}
	if *concurrency <= 0 {
		fmt.Fprintln(stderr, "bursttest: -concurrency must be positive")
		return exitUsage
	}

	target := strings.TrimRight(*baseURL, "/") + spec.path
	// Do not follow redirects and do not keep cookies: a redirect is not a 429,
	// and the probe must never carry state between requests.
	client := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	statuses, errs := burst(context.Background(), client, target, spec, *requests, *window, *concurrency)
	s := summarise(statuses, errs, *expectLimit)

	fmt.Fprintf(stdout, "bursttest: profile=%s method=%s url=%s requests=%d window=%s concurrency=%d expect-limit=%d\n",
		spec.name, spec.method, target, *requests, *window, *concurrency, *expectLimit)
	fmt.Fprintf(stdout, "total: %d\n", s.total)
	for _, code := range s.codes() {
		fmt.Fprintf(stdout, "status %d: %d\n", code, s.counts[code])
	}
	if s.first429 < 0 {
		fmt.Fprintln(stdout, "first 429: none")
	} else {
		fmt.Fprintf(stdout, "first 429: index %d\n", s.first429)
	}
	if s.errorCount > 0 {
		fmt.Fprintf(stdout, "transport errors: %d (first: %v)\n", s.errorCount, s.firstErr)
	}
	if s.pass {
		fmt.Fprintln(stdout, "result: PASS")
		return exitPass
	}
	fmt.Fprintln(stdout, "result: FAIL")
	return exitFail
}

func isLoopback(u *url.URL) bool {
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// burst spreads total requests evenly across window and sends up to
// concurrency at a time. Results keep the send order by request index.
func burst(ctx context.Context, client *http.Client, target string, spec profile, total int, window time.Duration, concurrency int) ([]int, []error) {
	statuses := make([]int, total)
	errs := make([]error, total)
	gap := window / time.Duration(total)
	start := time.Now()

	jobs := make(chan int)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if wait := time.Until(start.Add(time.Duration(i) * gap)); wait > 0 {
					time.Sleep(wait)
				}
				statuses[i], errs[i] = send(ctx, client, target, spec)
			}
		}()
	}
	for i := range total {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return statuses, errs
}

func send(ctx context.Context, client *http.Client, target string, spec profile) (int, error) {
	var body io.Reader
	if spec.method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, target, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	if spec.method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, nil
}

type summary struct {
	total      int
	counts     map[int]int
	first429   int
	errorCount int
	firstErr   error
	pass       bool
}

func (s summary) codes() []int {
	codes := make([]int, 0, len(s.counts))
	for code := range s.counts {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	return codes
}

// summarise applies the PASS rule: no 429 among the first expectLimit
// requests, at least one 429 after them, and no transport errors.
func summarise(statuses []int, errs []error, expectLimit int) summary {
	s := summary{total: len(statuses), counts: map[int]int{}, first429: -1}
	for i, code := range statuses {
		if errs[i] != nil {
			s.errorCount++
			if s.firstErr == nil {
				s.firstErr = errs[i]
			}
			continue
		}
		s.counts[code]++
		if code == http.StatusTooManyRequests && s.first429 < 0 {
			s.first429 = i
		}
	}
	s.pass = s.errorCount == 0 && s.first429 >= expectLimit
	return s
}

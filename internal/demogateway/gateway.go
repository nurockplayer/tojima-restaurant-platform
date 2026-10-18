// Package demogateway exposes only the disposable cash demo through an
// authenticated loopback proxy. It is never wired into cmd/server.
package demogateway

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const GateHeader = "X-Tojima-Demo-Gate"
const CookieName = "__Host-tojima_demo"
const MaxBodyBytes = 1 << 20

type Config struct {
	Listen       string
	Upstream     string
	PublicOrigin string
	Secret       string
}

// Validate rejects names requiring DNS, wildcard binds, URL credentials and
// URL suffixes. Errors deliberately never include configuration values.
func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || !loopback(host) || !validPort(port) {
		return errors.New("listen must be a literal loopback IP and numeric port")
	}
	u, err := exactOrigin(c.Upstream, "http")
	if err != nil || !loopback(u.Hostname()) {
		return errors.New("upstream must be a literal loopback http origin")
	}
	if _, err := exactOrigin(c.PublicOrigin, "https"); err != nil {
		return errors.New("public origin must be an exact https origin")
	}
	if len(c.Secret) < 32 || strings.ContainsAny(c.Secret, "\r\n") {
		return errors.New("TOJIMA_DEMO_GATE_SECRET must contain at least 32 bytes without line breaks")
	}
	return nil
}

func loopback(host string) bool {
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Zone() == "" && ip.IsLoopback()
}
func validPort(port string) bool {
	if port == "" || strings.Trim(port, "0123456789") != "" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}
func exactOrigin(raw, scheme string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.String() != raw || strings.ContainsAny(u.Host, "\\ \t\r\n") || strings.HasSuffix(u.Host, ":") || (u.Port() != "" && !validPort(u.Port())) {
		return nil, errors.New("invalid origin")
	}
	return u, nil
}

func New(c Config) (http.Handler, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	upstream, _ := url.Parse(c.Upstream)
	public, _ := url.Parse(c.PublicOrigin)
	secretHash := sha256.Sum256([]byte(c.Secret))
	proxy := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(upstream)
			p.Out.Host = upstream.Host
			stripHeaders(p.Out.Header)
			if p.In.Header.Get("Origin") != "" {
				p.Out.Header.Set("Origin", c.Upstream)
			}
			if ref, err := url.Parse(p.In.Referer()); err == nil && ref.Scheme == public.Scheme && ref.Host == public.Host && ref.User == nil {
				ref.Scheme, ref.Host = upstream.Scheme, upstream.Host
				p.Out.Header.Set("Referer", ref.String())
			} else {
				p.Out.Header.Del("Referer")
			}
		},
		// Never honor HTTP_PROXY: the target must stay on literal loopback.
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ResponseHeaderTimeout: 20 * time.Second, IdleConnTimeout: 60 * time.Second,
			MaxIdleConns: 16, MaxIdleConnsPerHost: 16, MaxConnsPerHost: 64,
		},
		ModifyResponse: func(r *http.Response) error {
			r.Header.Del(GateHeader)
			r.Header.Set("X-Robots-Tag", "noindex, nofollow")
			if strings.HasPrefix(r.Request.URL.Path, "/v1/") {
				r.Header.Set("Cache-Control", "no-store")
			}
			if loc, err := url.Parse(r.Header.Get("Location")); err == nil && loc.User == nil && loc.Host == upstream.Host && (loc.Scheme == upstream.Scheme || loc.Scheme == "") {
				loc.Scheme, loc.Host = public.Scheme, public.Host
				r.Header.Set("Location", loc.String())
			}
			return nil
		},
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "demo offline", http.StatusServiceUnavailable)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("Cache-Control", "no-store")
		supplied := sha256.Sum256([]byte(r.Header.Get(GateHeader)))
		if subtle.ConstantTimeCompare(supplied[:], secretHash[:]) != 1 || len(r.Header.Values(GateHeader)) != 1 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		origins := r.Header.Values("Origin")
		if len(origins) > 0 && (len(origins) != 1 || origins[0] != c.PublicOrigin) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !allowed(r) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.ContentLength > MaxBodyBytes {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		// Buffer at most 1 MiB before dialing, including chunked/unknown-size bodies.
		// A malicious body can never partially execute an upstream mutation.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		if err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "bad request", http.StatusBadRequest)
			}
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		r.Trailer = nil
		proxy.ServeHTTP(w, r)
	}), nil
}

func stripHeaders(h http.Header) {
	// ReverseProxy already removes hop-by-hop headers before Rewrite; also strip
	// explicitly here to keep this boundary clear and handle Connection tokens.
	for _, value := range h.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for name := range h {
		lower := strings.ToLower(name)
		if lower == strings.ToLower(GateHeader) || lower == "forwarded" || strings.HasPrefix(lower, "cf-") || strings.HasPrefix(lower, "x-forwarded-") {
			h.Del(name)
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(name)
	}
	var cookies []string
	for _, line := range h.Values("Cookie") {
		for _, item := range strings.Split(line, ";") {
			item = strings.TrimSpace(item)
			name, _, _ := strings.Cut(item, "=")
			if strings.TrimSpace(name) != CookieName && item != "" {
				cookies = append(cookies, item)
			}
		}
	}
	h.Del("Cookie")
	if len(cookies) > 0 {
		h.Set("Cookie", strings.Join(cookies, "; "))
	}
}

// Explicit method/path pairs from httpapi's routers and web/src/api. Keep the
// walkthrough narrow: no Stripe, webhook, test fixture, catalog publication,
// price editing, emergency hold, adjustment or refund mutation routes.
var demoRoutes = func() map[string][]*regexp.Regexp {
	const id = `[A-Za-z0-9_-]+`
	const guest = `/v1/guest/sessions/` + id
	const staff = `/v1/staff/tenants/` + id + `/stores/` + id
	const session = staff + `/tables/` + id + `/sessions/` + id
	const admin = `/v1/admin/tenants/` + id
	patterns := map[string][]string{
		"GET": {
			`/v1/guest/tables/` + id + `(/menu)?`, guest + `(/menu|/cart|/orders|/operations)?`,
			`/v1/guest/orders/` + id + `/status`,
			staff + `/operations/(access|tables|kds|reviews|cash-settings)`, session + `/operations`,
			admin + `/menu/(published|draft)`, admin + `/stores/` + id + `/menu(/draft)?`,
		},
		"POST": {
			`/v1/guest/tables/` + id + `/join`, guest + `/join`, guest + `/cart/(lines|preview|commit)`,
			guest + `/orders/` + id + `/read-grant`, staff + `/tables/` + id + `/sessions`,
			session + `/(close|cash/preview|cash/settle)`, session + `/orders/` + id + `/(accept|fulfillment)`,
		},
		"PUT":    {guest + `/cart/lines/` + id, admin + `/stores/` + id + `/menu/availability`},
		"DELETE": {guest + `/cart/lines/` + id},
	}
	routes := make(map[string][]*regexp.Regexp)
	for method, entries := range patterns {
		for _, p := range entries {
			routes[method] = append(routes[method], regexp.MustCompile(`^`+p+`$`))
		}
	}
	return routes
}()
var assetPath = regexp.MustCompile(`^/assets/[A-Za-z0-9_-]+\.(js|css|svg|png|jpg|jpeg|webp|ico|woff2?)$`)

func allowed(r *http.Request) bool {
	p := r.URL.Path
	if r.URL.EscapedPath() != p || path.Clean(p) != strings.TrimSuffix(p, "/") && p != "/" || strings.Contains(p, "\\") {
		return false
	}
	if r.Method == "GET" || r.Method == "HEAD" {
		switch p {
		case "/", "/staff", "/staff/", "/kitchen", "/kitchen/", "/catalog", "/catalog/", "/order", "/order/":
			return true
		}
		if assetPath.MatchString(p) {
			return true
		}
	}
	method := r.Method
	if method == "HEAD" {
		method = "GET"
	}
	for _, route := range demoRoutes[method] {
		if route.MatchString(p) {
			return true
		}
	}
	return false
}

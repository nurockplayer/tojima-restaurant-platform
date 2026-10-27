package staffauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
)

// Lazy serves OIDC routes and resolves sessions once discovery succeeds.
type Lazy struct {
	store           *Store
	cfg             Config
	h               atomic.Pointer[Handler]
	pendingMu       sync.Mutex
	pending         []pendingLogout
	pendingBytes    int
	pendingOverflow bool
}

type pendingLogout struct {
	body       string
	receivedAt time.Time
}

const (
	maxPendingLogoutBody  = 64 << 10
	maxPendingLogoutCount = 128
	maxPendingLogoutBytes = 1 << 20
)

// NewLazy validates configuration without contacting the identity provider.
func NewLazy(store *Store, cfg Config) (*Lazy, error) {
	if err := validateConfig(store, cfg); err != nil {
		return nil, err
	}
	return &Lazy{store: store, cfg: cfg}, nil
}

// Start launches discovery once per runtime, retrying until success or cancellation.
func (l *Lazy) Start(ctx context.Context) {
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			h, err := NewHandler(ctx, l.store, l.cfg)
			if err == nil {
				if ctx.Err() == nil {
					l.activate(ctx, h)
				}
				return
			}
			if ctx.Err() != nil {
				return
			}
			log.Print("staff auth discovery failed; retrying")
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()
}

func (l *Lazy) Mount(mux *http.ServeMux) {
	for _, route := range []string{"/staff/auth/login", "/staff/auth/step-up", CallbackPath, "/staff/auth/logout", "/staff/auth/session", "/staff/auth/backchannel-logout"} {
		mux.Handle(route, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := l.h.Load()
			if h == nil {
				if r.URL.Path == "/staff/auth/backchannel-logout" && r.Method == http.MethodPost {
					body, err := io.ReadAll(io.LimitReader(r.Body, maxPendingLogoutBody+1))
					if err == nil && len(body) > 0 && len(body) <= maxPendingLogoutBody {
						l.pendingMu.Lock()
						h = l.h.Load()
						if h == nil {
							if len(l.pending) < maxPendingLogoutCount && l.pendingBytes+len(body) <= maxPendingLogoutBytes {
								l.pending = append(l.pending, pendingLogout{body: string(body), receivedAt: time.Now()})
								l.pendingBytes += len(body)
							} else {
								l.pendingOverflow = true
							}
						}
						l.pendingMu.Unlock()
						if h != nil {
							r.Body = io.NopCloser(bytes.NewReader(body))
							l.serve(h, w, r)
							return
						}
					}
				}
				authJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "staff_auth_unavailable"})
				return
			}
			l.serve(h, w, r)
		}))
	}
}

func (l *Lazy) serve(h *Handler, w http.ResponseWriter, r *http.Request) {
	ready := http.NewServeMux()
	h.Mount(ready)
	ready.ServeHTTP(w, r)
}

// activate applies logout notifications received during cold-start discovery
// before making persisted sessions usable. The input is untrusted until the
// normal OIDC logout verifier processes it.
func (l *Lazy) activate(ctx context.Context, h *Handler) {
	backoff := time.Second
	for {
		l.pendingMu.Lock()
		if l.pendingOverflow {
			l.pendingMu.Unlock()
			return
		}
		if len(l.pending) == 0 {
			l.h.Store(h)
			l.pendingMu.Unlock()
			return
		}
		pending := l.pending
		l.pending = nil
		l.pendingBytes = 0
		l.pendingMu.Unlock()

		retry := make([]pendingLogout, 0)
		for _, entry := range pending {
			form, parseErr := url.ParseQuery(entry.body)
			var err error
			if parseErr != nil {
				err = errInvalidLogoutToken
			} else {
				_, err = h.applyBackchannelLogout(ctx, form.Get("logout_token"), entry.receivedAt)
			}
			if err != nil && !errors.Is(err, errInvalidLogoutToken) {
				retry = append(retry, entry)
			}
		}
		if len(retry) > 0 {
			l.pendingMu.Lock()
			if len(retry)+len(l.pending) > maxPendingLogoutCount {
				l.pendingOverflow = true
			} else {
				l.pending = append(retry, l.pending...)
				for _, entry := range retry {
					l.pendingBytes += len(entry.body)
				}
				if l.pendingBytes > maxPendingLogoutBytes {
					l.pendingOverflow = true
				}
			}
			overflow := l.pendingOverflow
			l.pendingMu.Unlock()
			if overflow {
				continue
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}
}

func (l *Lazy) Resolve(ctx context.Context, r *http.Request) (authz.Actor, error) {
	if l.h.Load() == nil {
		return authz.Actor{}, authz.ErrDenied
	}
	return resolve(ctx, l.store, r)
}

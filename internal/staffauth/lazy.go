package staffauth

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
)

// Lazy serves OIDC routes once discovery succeeds; Resolve, session inspection
// and authoritative local logout never need the IdP.
type Lazy struct {
	store *Store
	cfg   Config
	h     atomic.Pointer[Handler]
}

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
					l.h.Store(h)
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
	// A cold start during an IdP outage must not strand live sessions on shared
	// devices. Reuse the same method/Origin checks and store-only session/logout
	// handlers; only the best-effort RP logout redirect is unavailable here.
	local := http.NewServeMux()
	(&Handler{store: l.store, cfg: l.cfg}).Mount(local)
	for _, route := range []string{"/staff/auth/login", "/staff/auth/step-up", CallbackPath, "/staff/auth/logout", "/staff/auth/session", "/staff/auth/backchannel-logout"} {
		mux.Handle(route, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := l.h.Load()
			if h == nil {
				if r.URL.Path == "/staff/auth/session" || r.URL.Path == "/staff/auth/logout" {
					local.ServeHTTP(w, r)
					return
				}
				authJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "staff_auth_unavailable"})
				return
			}
			ready := http.NewServeMux()
			h.Mount(ready)
			ready.ServeHTTP(w, r)
		}))
	}
}

func (l *Lazy) Resolve(ctx context.Context, r *http.Request) (authz.Actor, error) {
	return resolve(ctx, l.store, r)
}

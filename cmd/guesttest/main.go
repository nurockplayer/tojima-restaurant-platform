// guesttest is an explicit loopback-only, disposable guest acceptance server.
// It does not configure production identity, hosting, guest policy or payments.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/httpapi"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	assets := flag.String("web", "web/dist", "built static guest assets")
	flag.Parse()
	index := filepath.Join(*assets, "index.html")
	if _, err := os.Stat(index); err != nil {
		return errors.New("build web assets first: pnpm --dir web build")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	f, err := testguest.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer f.Close(context.Background())
	mux := http.NewServeMux()
	mux.Handle("/v1/", httpapi.NewGuestHandler(f.Sessions, f.Menus, f.Orders, f.Operations))
	mux.Handle("/", guestAssets(*assets))
	server := &http.Server{Addr: "127.0.0.1:8081", ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Println("Isolated P78 test guest: http://127.0.0.1:8081/?table=" + f.Locator)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// The fragment is client-only, but its /order entry path must serve the app
// for a copied link opened directly in this isolated acceptance runtime.
func guestAssets(root string) http.Handler {
	mux := http.NewServeMux()
	order := func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	}
	mux.HandleFunc("GET /order", order)
	mux.HandleFunc("GET /order/{$}", order)
	mux.Handle("/", http.FileServer(http.Dir(root)))
	return mux
}

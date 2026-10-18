package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCopiedOrderLinkDirectEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("guest-app"), 0600); err != nil {
		t.Fatal(err)
	}
	h := guestAssets(root)
	for _, path := range []string{"/order", "/order/"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 || w.Body.String() != "guest-app" {
			t.Fatal("direct order link did not serve the guest app")
		}
	}
}

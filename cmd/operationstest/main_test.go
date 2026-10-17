package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testoperations"
)

func TestCredentialOutputIsExclusiveOwnerOnlyAndRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	doc := credentialDocument{Credentials: testoperations.CredentialSet{Cashier: testoperations.Credential{Role: authz.Staff, Token: "secret"}}}
	owned, err := writeCredentials(path, doc)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credentials permissions=%#o, want 0600", got)
	}
	if _, err := writeCredentials(path, doc); err == nil {
		t.Fatal("existing credential path was overwritten")
	}
	owned.removeIfOwned()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owned credentials file remains after cleanup: %v", err)
	}
}

func TestCredentialsOutsideServedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "credentials.json")
	if err := validateCredentialsOutsideWebRoot(inside, root); err == nil {
		t.Fatal("credentials under the served directory were accepted")
	}
	outside := filepath.Join(t.TempDir(), "credentials.json")
	if err := validateCredentialsOutsideWebRoot(outside, root); err != nil {
		t.Fatalf("outside credential path rejected: %v", err)
	}
}

func TestSPAFallbackServesStaffRouteAndRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("spa-index"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "secret.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	handler := spaHandler(root)
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/staff", want: "spa-index"},
		{path: "/secret.txt", want: "spa-index"},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK || response.Body.String() != test.want {
				t.Fatalf("response=%d %q, want %q", response.Code, response.Body.String(), test.want)
			}
		})
	}
}

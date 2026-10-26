package main

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

var viteAsset = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9.]+$`)

// os.Root prevents traversal and symlink escapes, including during file opens.
// Existing directories are never handed to FileServer (which lists them).
func newSPAHandler(directory string) (http.Handler, func(), error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, nil, errors.New("APP_WEB_DIST must be a readable built SPA directory")
	}
	closeRoot := func() { _ = root.Close() }
	index, err := root.Open("index.html")
	if err != nil {
		closeRoot()
		return nil, nil, errors.New("APP_WEB_DIST must contain a readable index.html")
	}
	info, err := index.Stat()
	_ = index.Close()
	if err != nil || !info.Mode().IsRegular() {
		closeRoot()
		return nil, nil, errors.New("APP_WEB_DIST index.html must be a regular file")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		for _, segment := range strings.Split(name, "/") {
			if strings.HasPrefix(segment, ".") || strings.Contains(segment, "\\") {
				http.NotFound(w, r)
				return
			}
		}
		file, err := root.Open(name)
		if err != nil {
			// Only missing navigation paths use the shell. Missing scripts/styles and
			// unknown API paths must retain their 404 rather than returning HTML.
			accept := r.Header.Get("Accept")
			if !os.IsNotExist(err) || path.Ext(name) != "" || strings.HasPrefix(name, "assets/") || (accept != "" && !strings.Contains(accept, "text/html") && !strings.Contains(accept, "*/*")) {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
			file, err = root.Open(name)
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(name, "assets/") && viteAsset.MatchString(path.Base(name)) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		http.ServeContent(w, r, name, info.ModTime(), file)
	})
	return handler, closeRoot, nil
}

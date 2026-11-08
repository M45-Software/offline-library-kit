package main

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The custom homepage is localhost-only; avoid directory listings, hidden
// files, partial archives, credentials, and symlinks leaving the library.
func homepageHandler(root string) (http.Handler, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	server := http.FileServer(http.Dir(canonical))
	within := func(candidate string) bool {
		resolved, e := filepath.EvalSymlinks(candidate)
		if e != nil {
			return false
		}
		relative, e := filepath.Rel(canonical, resolved)
		return e == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:8765" || r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if strings.Contains(r.URL.Path, `\`) {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		for _, part := range parts {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		lower := strings.ToLower(clean)
		base := strings.ToLower(path.Base(clean))
		if lower == "content/zim" || strings.HasPrefix(lower, "content/zim/") || strings.Contains(base, "password") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".part") {
			http.NotFound(w, r)
			return
		}
		if clean == "." {
			clean = ""
		}
		target := filepath.Join(canonical, filepath.FromSlash(clean))
		if !within(target) {
			http.NotFound(w, r)
			return
		}
		info, e := os.Stat(target)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		if info.IsDir() && !within(filepath.Join(target, "index.html")) {
			http.NotFound(w, r)
			return
		}
		server.ServeHTTP(w, r)
	}), nil
}

package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHomepageServesLibraryAndBlocksPrivatePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("home"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hotspot-password.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "content", "zim"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "content", "zim", "private.zim.part"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	h, err := homepageHandler(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, host string
		status     int
	}{{"/", "127.0.0.1:8765", 200}, {"/.env", "127.0.0.1:8765", 404}, {"/hotspot-password.txt", "127.0.0.1:8765", 404}, {"/content/zim/private.zim.part", "127.0.0.1:8765", 404}, {"/empty/", "127.0.0.1:8765", 404}, {"/", "evil.test:8765", 403}} {
		r := httptest.NewRequest("GET", "http://"+tc.host+tc.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s = %d; want %d", tc.host, tc.path, w.Code, tc.status)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err = os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.txt")
	if err = os.Symlink(outside, link); err == nil {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8765/escape.txt", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("outside symlink served: %d", w.Code)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscardRequiresMatchingPartialAndPreservesFinal(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	r := downloadRequest{URL: "https://example.com/a", Name: "file.zim", SHA256: strings.Repeat("a", 64), Bytes: 9}
	part := filepath.Join(dir, r.Name+".part")
	if err := os.WriteFile(part, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(resumeInfo(r))
	if err := os.WriteFile(part+".json", metadata, 0600); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, r.Name)
	if err := os.WriteFile(final, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	wrong := r
	wrong.SHA256 = strings.Repeat("b", 64)
	if err := discardPartial(root, wrong); err == nil {
		t.Fatal("discarded mismatched partial")
	}
	if _, err := os.Stat(part); err != nil {
		t.Fatal("mismatched partial removed")
	}
	if err := discardPartial(root, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatal("partial remains")
	}
	if _, err := os.Stat(part + ".json"); !os.IsNotExist(err) {
		t.Fatal("metadata remains")
	}
	b, _ := os.ReadFile(final)
	if string(b) != "keep" {
		t.Fatal("final file changed")
	}
}
func TestRetryAndDiscardAPI(t *testing.T) {
	payload := []byte("retry fixture")
	digest := sha256.Sum256(payload)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Write(payload[:3])
			return
		}
		if requests == 2 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("Range") != "bytes=3-" {
			t.Errorf("unexpected range: %s", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 3-%d/%d", len(payload)-1, len(payload)))
		w.WriteHeader(206)
		w.Write(payload[3:])
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), token: "secret"}
	item := downloadRequest{URL: server.URL, Name: "retry.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	if !a.beginDownload(job{State: "checking"}, func(context.Context) (downloadRequest, error) { return item, nil }) {
		t.Fatal("start failed")
	}
	waitForJob(t, a, "failed")
	call := func(method, endpoint string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:9999"+endpoint, nil)
		r.Header.Set("X-Library-Token", "secret")
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		return w
	}
	var state map[string]any
	if err := json.Unmarshal(call("GET", "/api/state").Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["canRetry"] != true || state["canDiscard"] != true {
		t.Fatalf("missing recovery options: %+v", state)
	}
	if w := call("POST", "/api/download/retry"); w.Code != 200 {
		t.Fatalf("retry rejected: %d %s", w.Code, w.Body)
	}
	waitForJob(t, a, "complete")
	b, err := os.ReadFile(filepath.Join(a.root, "content", "zim", item.Name))
	if err != nil || string(b) != string(payload) {
		t.Fatalf("bad installed file: %q %v", b, err)
	}
	if w := call("POST", "/api/download/discard"); w.Code != 409 {
		t.Fatalf("discard allowed after success: %d", w.Code)
	}
}
func TestRetryAfterCatalogResolutionFailure(t *testing.T) {
	payload := []byte("catalog retry fixture")
	digest := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), token: "secret"}
	item := downloadRequest{URL: server.URL, Name: "catalog.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	attempts := 0
	if !a.beginDownload(job{State: "resolving"}, func(context.Context) (downloadRequest, error) {
		attempts++
		if attempts == 1 {
			return downloadRequest{}, errors.New("temporary catalog error")
		}
		return item, nil
	}) {
		t.Fatal("start failed")
	}
	waitForJob(t, a, "failed")
	call := func(method, endpoint string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:9999"+endpoint, nil)
		r.Header.Set("X-Library-Token", "secret")
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		return w
	}
	var state map[string]any
	if err := json.Unmarshal(call("GET", "/api/state").Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["canRetry"] != true || state["canDiscard"] != false {
		t.Fatalf("unexpected recovery options: %+v", state)
	}
	if w := call("POST", "/api/download/retry"); w.Code != 200 {
		t.Fatalf("retry rejected: %d %s", w.Code, w.Body)
	}
	waitForJob(t, a, "complete")
	b, err := os.ReadFile(filepath.Join(a.root, "content", "zim", item.Name))
	if err != nil || string(b) != string(payload) || attempts != 2 {
		t.Fatalf("bad retry: %q %v attempts=%d", b, err, attempts)
	}
}

func TestRetryRechecksChangedPublisherDetails(t *testing.T) {
	payload := []byte("original catalog payload")
	digest := sha256.Sum256(payload)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write(payload[:3])
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), token: "secret"}
	item := downloadRequest{URL: server.URL, Name: "changed.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	attempts := 0
	if !a.beginDownload(job{State: "resolving"}, func(context.Context) (downloadRequest, error) {
		attempts++
		if attempts > 1 {
			item.SHA256 = strings.Repeat("a", 64)
		}
		return item, nil
	}) {
		t.Fatal("start failed")
	}
	waitForJob(t, a, "failed")
	part := filepath.Join(a.root, "content", "zim", item.Name+".part")
	before, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:9999/api/download/retry", nil)
	r.Header.Set("X-Library-Token", "secret")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("retry rejected: %d %s", w.Code, w.Body)
	}
	waitForJob(t, a, "failed")
	a.mu.Lock()
	message := a.current.Error
	a.mu.Unlock()
	after, err := os.ReadFile(part)
	if err != nil || !bytes.Equal(before, after) || requests != 2 || !strings.Contains(message, "different download") {
		t.Fatalf("changed metadata was not safely rejected: %q %v requests=%d error=%q", after, err, requests, message)
	}
}

func waitForJob(t *testing.T, a *app, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		state, msg := a.current.State, a.current.Error
		a.mu.Unlock()
		if state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s: %s %s", want, state, msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSavedPartialListingAndRedaction(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	request := downloadRequest{URL: "https://example.com/file?secret=hidden", Name: "saved.zim", Bytes: 20, SHA256: strings.Repeat("a", 64), CatalogID: "600c47c6-a792-0f78-8fcc-9c05cf1c556b"}
	part := filepath.Join(dir, request.Name+".part")
	if err := os.WriteFile(part, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	record := resumeInfo(request)
	data, _ := json.Marshal(record)
	if err := os.WriteFile(part+".json", data, 0600); err != nil {
		t.Fatal(err)
	}
	items := listSavedPartials(root)
	if len(items) != 1 || !items[0].CanResume || items[0].Done != 7 || items[0].Total != 20 {
		t.Fatalf("bad listing: %+v", items)
	}
	encoded, _ := json.Marshal(items)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), record.URLHash) || strings.Contains(string(encoded), request.CatalogID) {
		t.Fatalf("private metadata exposed: %s", encoded)
	}
	if _, ok := savedPartialByName(root, "../saved.zim"); ok {
		t.Fatal("accepted traversal")
	}
	if err := os.WriteFile(part+".json", []byte(`{"name":"other.zim"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if len(listSavedPartials(root)) != 0 {
		t.Fatal("listed mismatched metadata")
	}
}

func TestSavedManualPartialCannotResumeWithoutDetails(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	r := downloadRequest{URL: "https://example.com/a", Name: "manual.zim", Bytes: 20, SHA256: strings.Repeat("b", 64)}
	part := filepath.Join(dir, r.Name+".part")
	if err := os.WriteFile(part, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(resumeInfo(r))
	if err := os.WriteFile(part+".json", data, 0600); err != nil {
		t.Fatal(err)
	}
	items := listSavedPartials(root)
	if len(items) != 1 || items[0].CanResume {
		t.Fatalf("manual partial misclassified: %+v", items)
	}
}

func TestResumeCatalogPartialAfterRestart(t *testing.T) {
	payload := []byte("saved after restart")
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	id := "600c47c6-a792-0f78-8fcc-9c05cf1c556b"
	item := downloadRequest{URL: "https://download.kiwix.org/zim/example/example.zim", Name: "example.zim", Bytes: int64(len(payload)), SHA256: hash, CatalogID: id}
	entry := `<entry><id>urn:uuid:` + id + `</id><link rel="http://opds-spec.org/acquisition/open-access" type="application/x-zim" href="https://download.kiwix.org/zim/example/example.zim.meta4"/></entry>`
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(dir, item.Name+".part")
	if err := os.WriteFile(part, payload[:5], 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(resumeInfo(item))
	if err := os.WriteFile(part+".json", data, 0600); err != nil {
		t.Fatal(err)
	}
	publisherHash := strings.Repeat("b", 64)
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "library.kiwix.org":
			return response(entry), nil
		case strings.HasSuffix(r.URL.Path, ".meta4"):
			return response(fmt.Sprintf(`<metalink><file name="example.zim"><size>%d</size><hash type="sha-256">%s</hash></file></metalink>`, len(payload), publisherHash)), nil
		case r.URL.Path == "/zim/example/example.zim":
			if r.Header.Get("Range") != "bytes=5-" {
				t.Errorf("wrong range %s", r.Header.Get("Range"))
			}
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": []string{fmt.Sprintf("bytes 5-%d/%d", len(payload)-1, len(payload))}}, Body: io.NopCloser(bytes.NewReader(payload[5:]))}, nil
		default:
			t.Errorf("unexpected request %s", r.URL)
			return nil, errors.New("unexpected request")
		}
	})}
	a := &app{root: root, client: client, token: "secret", current: job{State: "idle"}}
	r := httptest.NewRequest("POST", "http://127.0.0.1:9999/api/download/resume-saved", strings.NewReader(`{"name":"example.zim"}`))
	r.Header.Set("X-Library-Token", "secret")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	waitForJob(t, a, "failed")
	if got, err := os.ReadFile(part); err != nil || !bytes.Equal(got, payload[:5]) {
		t.Fatalf("changed publisher metadata modified partial: %q %v", got, err)
	}
	publisherHash = hash
	r = httptest.NewRequest("POST", "http://127.0.0.1:9999/api/download/resume-saved", strings.NewReader(`{"name":"example.zim"}`))
	r.Header.Set("X-Library-Token", "secret")
	w = httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("second resume: %d %s", w.Code, w.Body)
	}
	waitForJob(t, a, "complete")
	got, err := os.ReadFile(filepath.Join(dir, item.Name))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("bad result: %q %v", got, err)
	}
}

func TestDiscardSavedPartialAfterRestart(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	item := downloadRequest{URL: "https://example.com/file", Name: "manual.zim", Bytes: 20, SHA256: strings.Repeat("a", 64)}
	part := filepath.Join(dir, item.Name+".part")
	if err := os.WriteFile(part, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(resumeInfo(item))
	if err := os.WriteFile(part+".json", data, 0600); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, item.Name)
	if err := os.WriteFile(final, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	saved, ok := savedPartialByName(root, item.Name)
	if !ok {
		t.Fatal("partial not found")
	}
	if err := os.WriteFile(part, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := discardSavedPartial(root, item.Name, saved.Fingerprint); err == nil {
		t.Fatal("accepted stale fingerprint")
	}
	got, err := os.ReadFile(part)
	if err != nil || string(got) != "changed" {
		t.Fatalf("changed partial lost: %q %v", got, err)
	}
	saved, ok = savedPartialByName(root, item.Name)
	if !ok {
		t.Fatal("changed partial not found")
	}
	unlock, err := lockDownload(part + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := discardSavedPartial(root, item.Name, saved.Fingerprint); err == nil {
		t.Fatal("discard ignored active lock")
	}
	unlock()
	a := &app{root: root, token: "secret", current: job{State: "idle"}}
	body, _ := json.Marshal(map[string]string{"name": item.Name, "fingerprint": saved.Fingerprint})
	r := httptest.NewRequest("POST", "http://127.0.0.1:9999/api/download/discard-saved", bytes.NewReader(body))
	r.Header.Set("X-Library-Token", "secret")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("discard: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatal("partial remains")
	}
	if _, err := os.Stat(part + ".json"); !os.IsNotExist(err) {
		t.Fatal("metadata remains")
	}
	got, err = os.ReadFile(final)
	if err != nil || string(got) != "keep" {
		t.Fatalf("completed file changed: %q %v", got, err)
	}
}

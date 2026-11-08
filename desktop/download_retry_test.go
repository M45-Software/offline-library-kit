package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutomaticDownloadRetryResumesMatchingPartial(t *testing.T) {
	payload := []byte("automatic resume fixture")
	hash := sha256.Sum256(payload)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Write(payload[:4])
			return
		}
		if r.Header.Get("Range") != "bytes=4-" {
			t.Errorf("retry did not resume: %s", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 4-%d/%d", len(payload)-1, len(payload)))
		w.WriteHeader(206)
		w.Write(payload[4:])
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client()}
	item := downloadRequest{URL: server.URL, Name: "resume.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:])}
	if err := a.downloadWithRetries(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(a.root, "content", "zim", item.Name))
	if err != nil || string(data) != string(payload) || requests.Load() != 2 {
		t.Fatalf("resume: %q %v requests=%d", data, err, requests.Load())
	}
}
func TestAutomaticRetryDoesNotRetryIntegrityOrRangeConflicts(t *testing.T) {
	for _, badRange := range []bool{false, true} {
		t.Run(fmt.Sprint(badRange), func(t *testing.T) {
			payload := []byte("integrity fixture")
			hash := sha256.Sum256(payload)
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(payload) }))
			defer server.Close()
			a := &app{root: t.TempDir(), client: server.Client()}
			item := downloadRequest{URL: server.URL, Name: "file.zim", Bytes: int64(len(payload)), SHA256: strings.Repeat("a", 64)}
			if badRange {
				item.SHA256 = hex.EncodeToString(hash[:])
				dir := filepath.Join(a.root, "content", "zim")
				os.MkdirAll(dir, 0700)
				os.WriteFile(filepath.Join(dir, item.Name+".part"), payload[:4], 0600)
				metadata := resumeInfo(item)
				if err := replaceResumeInfo(dir, filepath.Join(dir, item.Name+".part.json"), metadata); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.downloadWithRetries(context.Background(), item); err == nil {
				t.Fatal("invalid download accepted")
			}
			if requests.Load() != 1 {
				t.Fatal("retried an integrity or range conflict")
			}
		})
	}
}
func TestRetryBackoffCanBePausedImmediately(t *testing.T) {
	a := &app{}
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	start := time.Now()
	err := a.withNetworkRetries(ctx, "test", func() error {
		attempts++
		cancel()
		return &transientDownloadError{err: fmt.Errorf("connection reset")}
	})
	if err != context.Canceled || attempts != 1 || time.Since(start) > time.Second {
		t.Fatalf("pause did not stop retry: %v %d", err, attempts)
	}
}

func TestAutomaticRetriesAreBounded(t *testing.T) {
	a := &app{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	attempts := 0
	err := a.withNetworkRetries(ctx, "unavailable", func() error { attempts++; return &transientDownloadError{err: fmt.Errorf("server unavailable")} })
	var exhausted *exhaustedNetworkRetriesError
	if !errors.As(err, &exhausted) || attempts != 4 {
		t.Fatalf("retry limit: attempts=%d err=%v", attempts, err)
	}
}

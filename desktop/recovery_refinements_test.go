package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDriveReplacementDuringDownloadStopsAndOriginalCanResume(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "library")
	old := filepath.Join(parent, "disconnected")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := directoryIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("recovery"), 512*1024)
	hash := sha256.Sum256(payload)
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: 200, Header: make(http.Header)}
		data := payload
		if r.Header.Get("Range") != "" {
			if r.Header.Get("Range") != "bytes=1048576-" {
				t.Error(r.Header.Get("Range"))
			}
			data = payload[1048576:]
			response.StatusCode = 206
			response.Header.Set("Content-Range", "bytes 1048576-4194303/4194304")
		}
		response.Body = io.NopCloser(bytes.NewReader(data))
		return response, nil
	})}
	a := &app{root: root, rootIdentity: identity, client: client}
	changed := false
	a.jobSink = func(j job) {
		if !changed && j.State == "downloading" && j.Done > 0 {
			changed = true
			if err := os.Rename(root, old); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(root, 0700); err != nil {
				t.Error(err)
			}
		}
	}
	item := downloadRequest{Name: "recovery.zim", Bytes: int64(len(payload)), URL: "https://example.invalid/recovery", SHA256: hex.EncodeToString(hash[:])}
	if err := a.download(context.Background(), item); err == nil {
		t.Fatal("continued after drive replacement")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("wrote to replacement drive")
	}
	part := filepath.Join(old, "content", "zim", item.Name+".part")
	if info, err := os.Stat(part); err != nil || info.Size() != 1048576 {
		t.Fatalf("original partial not preserved: %v %v", info, err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old, root); err != nil {
		t.Fatal(err)
	}
	a.jobSink = nil
	if err := a.download(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(root, "content", "zim", item.Name))
	if err != nil || !bytes.Equal(actual, payload) {
		t.Fatal("reconnected library did not resume correctly")
	}
}

func TestInterruptedCopyPreservesCompletedFilesAndCanRestart(t *testing.T) {
	for _, phase := range []string{"copy", "verification", "disconnect"} {
		t.Run(phase, func(t *testing.T) {
			root, dest := t.TempDir(), t.TempDir()
			source := filepath.Join(root, "large.zim")
			payload := bytes.Repeat([]byte("copy"), 1024*1024)
			if err := os.WriteFile(source, payload, 0600); err != nil {
				t.Fatal(err)
			}
			completed := filepath.Join(dest, "completed.zim")
			if err := os.WriteFile(completed, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			identity, err := directoryIdentity(dest)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			moved := dest + "-disconnected"
			defer os.RemoveAll(moved)
			interrupted := false
			a := &app{root: root}
			a.jobSink = func(j job) {
				wanted := j.State == "downloading"
				if phase == "verification" {
					wanted = j.State == "verifying"
				}
				if wanted && !interrupted {
					interrupted = true
					if phase == "disconnect" {
						if err := os.Rename(dest, moved); err != nil {
							t.Error(err)
						}
						if err := os.Mkdir(dest, 0700); err != nil {
							t.Error(err)
						}
					} else {
						cancel()
					}
				}
			}
			check := func() error {
				if err := ctx.Err(); err != nil {
					return err
				}
				current, err := directoryIdentity(dest)
				if err != nil || !os.SameFile(identity, current) {
					return errors.New("destination disconnected")
				}
				return nil
			}
			if err := a.copyOneStaged(ctx, source, dest, check, 2); err == nil {
				t.Fatal("interrupted copy installed archive")
			}
			if phase == "disconnect" {
				if entries, _ := os.ReadDir(dest); len(entries) != 0 {
					t.Fatal("wrote replacement destination")
				}
				if err := os.Remove(dest); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(moved, dest); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(filepath.Join(dest, "large.zim")); !os.IsNotExist(err) {
				t.Fatal("partial installed as final")
			}
			a.jobSink = nil
			if err := a.copyOneStaged(context.Background(), source, dest, func() error { return nil }, 2); err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(filepath.Join(dest, "large.zim"))
			if err != nil || !bytes.Equal(actual, payload) {
				t.Fatal("copy restart corrupt")
			}
			actual, _ = os.ReadFile(completed)
			if string(actual) != "keep" {
				t.Fatal("completed destination changed")
			}
			actual, _ = os.ReadFile(source)
			if !bytes.Equal(actual, payload) {
				t.Fatal("source changed")
			}
		})
	}
}

func TestStagingCopyCrashHelper(t *testing.T) {
	root := os.Getenv("OLK_COPY_CRASH_ROOT")
	if root == "" {
		return
	}
	a := &app{root: root}
	a.jobSink = func(j job) {
		if j.State == "downloading" && j.Done > 0 {
			if err := os.WriteFile(filepath.Join(root, "ready"), []byte("ready"), 0600); err != nil {
				t.Fatal(err)
			}
			select {}
		}
	}
	if err := a.copyOneStaged(context.Background(), filepath.Join(root, "source.zim"), filepath.Join(root, "destination"), func() error { return nil }, 2); err != nil {
		t.Fatal(err)
	}
}

func TestSearchAssetsStopWhenStagingDestinationIsReplaced(t *testing.T) {
	root, dest := t.TempDir(), filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "search")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("index"), 512*1024)
	if err := os.WriteFile(filepath.Join(source, "index.dat"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := directoryIdentity(dest)
	if err != nil {
		t.Fatal(err)
	}
	old := dest + "-disconnected"
	check := func() error {
		current, err := directoryIdentity(dest)
		if err != nil || !os.SameFile(identity, current) {
			return errors.New("destination changed")
		}
		return nil
	}
	a := &app{root: root}
	changed := false
	a.jobSink = func(j job) {
		if !changed && j.State == "indexing" && j.Done > 0 {
			changed = true
			if err := os.Rename(dest, old); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(dest, 0700); err != nil {
				t.Error(err)
			}
		}
	}
	target := filepath.Join(dest, "content", "search")
	if err := a.copySearchTreeChecked(context.Background(), source, target, 2, check); err == nil {
		t.Fatal("search copy ignored destination replacement")
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatal("search copy wrote replacement destination")
	}
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old, dest); err != nil {
		t.Fatal(err)
	}
	a.jobSink = nil
	if err := a.copySearchTreeChecked(context.Background(), source, target, 2, check); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(target, "index.dat"))
	if err != nil || !bytes.Equal(actual, payload) {
		t.Fatal("search copy did not recover")
	}
}

func TestCopyRestartsAfterAbruptProcessTermination(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "destination")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("crashcopy"), 512*1024)
	source := filepath.Join(root, "source.zim")
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStagingCopyCrashHelper$")
	cmd.Env = append(os.Environ(), "OLK_COPY_CRASH_ROOT="+root)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("copy helper did not reach write: %s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".part") {
		t.Fatal("crash did not preserve orphan partial")
	}
	orphan := filepath.Join(dest, entries[0].Name())
	before, err := os.ReadFile(orphan)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{root: root}
	if err := a.copyOneStaged(context.Background(), source, dest, func() error { return nil }, 2); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(dest, "source.zim"))
	if err != nil || !bytes.Equal(actual, payload) {
		t.Fatal("restart did not verify/install copy")
	}
	after, err := os.ReadFile(orphan)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("questionable orphan was removed or reused")
	}
}

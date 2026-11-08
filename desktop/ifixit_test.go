package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"offline-library-kit/desktop/internal/zimfixture"
)

func TestIFixitQueueRepairsAutomaticallyAndRetainsOriginal(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "fixture.zim")
	if err := zimfixture.Write(fixture); err != nil {
		t.Fatal(err)
	}
	payload, _ := os.ReadFile(fixture)
	digest := sha256.Sum256(payload)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write(payload)
	}))
	defer server.Close()
	a := &app{root: t.TempDir(), client: server.Client(), catalogQueue: []queuedCollection{{ID: queueID1, Title: "iFixit"}}}
	initializeTestQueue(t, a)
	item := downloadRequest{URL: server.URL + "/ifixit.zim", Name: "ifixit_test.zim", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), CatalogID: queueID1}
	resolve := func(context.Context, string) (downloadRequest, error) { return item, nil }
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, a)
	if a.current.State != "complete" {
		t.Fatalf("%+v", a.current)
	}
	dir := filepath.Join(a.root, "content", "zim")
	source := filepath.Join(dir, item.Name)
	check := func() {
		t.Helper()
		original, err := os.ReadFile(source)
		if err != nil || !bytes.Equal(original, payload) {
			t.Fatal("original changed", err)
		}
		if !completedRepair(dir, item.Name) {
			t.Fatal("missing completion record")
		}
		archives, err := libraryArchives(a.root)
		if err != nil || len(archives) != 1 || filepath.Base(archives[0]) != repairedName(item.Name) {
			t.Fatalf("%v %v", archives, err)
		}
	}
	check()
	encoded, _ := os.ReadFile(source + ".repair.json")
	var receipt repairReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Report.Links != 1 || receipt.Report.Notices != 2 || receipt.Report.MissingContentRecovered {
		t.Fatalf("%+v", receipt.Report)
	}
	if err := verifyRepairReadback(context.Background(), filepath.Join(dir, repairedName(item.Name)), receipt.Output.Checksum, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(filepath.Join(dir, repairedName(item.Name)))
	// A restart/reselection verifies and reuses both files without a new download.
	a.catalogQueue = []queuedCollection{{ID: queueID1, Title: "iFixit"}}
	if err := a.startCatalogQueue(resolve); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, a)
	check()
	after, _ := os.Stat(filepath.Join(dir, repairedName(item.Name)))
	if requests.Load() != 1 || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("completed repair was repeated")
	}
	// A stale receipt must never hide the publisher original.
	if err := os.WriteFile(source+".repair.json", []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	archives, _ := libraryArchives(a.root)
	if len(archives) != 2 {
		t.Fatal("stale receipt hid the original")
	}
}

func TestIFixitRepairProtectsSpaceAndExistingOutput(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(map[bool]string{false: "reserve", true: "existing output"}[collision], func(t *testing.T) {
			a := &app{root: t.TempDir(), reserveGiB: maxReserveGiB}
			dir := filepath.Join(a.root, "content", "zim")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			name := "ifixit_test.zim"
			source := filepath.Join(dir, name)
			if err := zimfixture.Write(source); err != nil {
				t.Fatal(err)
			}
			original, _ := os.ReadFile(source)
			final := filepath.Join(dir, repairedName(name))
			if collision {
				a.reserveGiB = 2
				if err := os.WriteFile(final, []byte("keep me"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.repairIFixit(context.Background(), downloadRequest{Name: name}); err == nil {
				t.Fatal("unsafe repair accepted")
			}
			after, _ := os.ReadFile(source)
			if !bytes.Equal(original, after) {
				t.Fatal("source changed")
			}
			if collision {
				out, _ := os.ReadFile(final)
				if string(out) != "keep me" {
					t.Fatal("output replaced")
				}
			}
			partials, _ := filepath.Glob(filepath.Join(dir, ".ifixit-repair-*.part"))
			if len(partials) != 0 {
				t.Fatal("preflight wrote a partial")
			}
		})
	}
}

func TestRepairReadbackRejectsTampering(t *testing.T) {
	name := filepath.Join(t.TempDir(), "fixture.zim")
	if err := zimfixture.Write(name); err != nil {
		t.Fatal(err)
	}
	stamp, err := stampRepairArchive(name)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(name, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("tampered"), 100)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyRepairReadback(context.Background(), name, stamp.Checksum, nil); err == nil {
		t.Fatal("tampered output accepted")
	}
	if !activeDownload("repairing") {
		t.Fatal("repair does not block conflicting jobs")
	}
}

// Optional large integration run, supplied explicitly by a developer. Neither
// publisher content nor a repaired archive is stored in the source checkout.
func TestIFixitRealArchive(t *testing.T) {
	source, root := os.Getenv("OFFLINE_LIBRARY_IFIXIT_SOURCE"), os.Getenv("OFFLINE_LIBRARY_IFIXIT_OUTPUT")
	if source == "" || root == "" {
		t.Skip("real archive integration inputs not supplied")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "content", "zim")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := "ifixit-mirror.zim"
	if err := os.Link(source, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	a := &app{root: root}
	if err := a.repairIFixit(context.Background(), downloadRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	if !completedRepair(dir, name) {
		t.Fatal("completion receipt not valid")
	}
	archives, err := libraryArchives(root)
	if err != nil || len(archives) != 1 {
		t.Fatalf("%v %v", archives, err)
	}
	t.Log("Validated repaired archive:", archives[0])
}

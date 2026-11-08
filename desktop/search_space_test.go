package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestGutenbergProfilesCoverMeasuredBuilds(t *testing.T) {
	data, err := os.ReadFile("../docs/gutenberg-space-measurements.json")
	if err != nil {
		t.Fatal(err)
	}
	var measurements struct {
		Collections []struct {
			Name        string `json:"name"`
			SourceBytes int64  `json:"sourceBytes"`
			ArchiveUUID string `json:"archiveUUID"`
			Peak        uint64 `json:"peakAllocatedBytes"`
			Finished    uint64 `json:"finishedBytes"`
		} `json:"collections"`
	}
	if err := json.Unmarshal(data, &measurements); err != nil {
		t.Fatal(err)
	}
	if len(measurements.Collections) != 12 {
		t.Fatalf("expected all 12 curated subjects, got %d", len(measurements.Collections))
	}
	for _, measured := range measurements.Collections {
		profile := collectionSearchSpace(measured.Name+".zim", measured.SourceBytes)
		if float64(profile.PeakBytes) < float64(measured.Peak)*1.25 || float64(profile.FinishedBytes) < float64(measured.Finished)*1.25 {
			t.Fatalf("profile underestimates %s: %+v", measured.Name, profile)
		}
		found := false
		for _, specific := range searchSpaceProfiles.Collections {
			if specific.CatalogID == measured.ArchiveUUID {
				found = true
				if specific.searchSpaceEstimate != profile {
					t.Fatalf("filename and catalog estimates differ for %s", measured.Name)
				}
			}
		}
		if !found {
			t.Fatalf("no subject profile for %s", measured.Name)
		}
	}
}

func TestSearchSpaceProfilesAreUsable(t *testing.T) {
	check := func(label string, profile searchSpaceEstimate) {
		t.Helper()
		if profile.FinishedBytes == 0 || profile.PeakBytes < profile.FinishedBytes || profile.PeakBytes > 1<<53 {
			t.Fatalf("invalid space profile %s: %+v", label, profile)
		}
	}
	check("default", searchSpaceProfiles.Default)
	for name, profile := range searchSpaceProfiles.Families {
		check(name, profile)
	}
	ids, prefixes := map[string]bool{}, map[string]bool{}
	for _, profile := range searchSpaceProfiles.Collections {
		check(profile.Prefix, profile.searchSpaceEstimate)
		if profile.Prefix == "" || profile.Prefix != strings.ToLower(profile.Prefix) || prefixes[profile.Prefix] || ids[profile.CatalogID] || !uuidPattern.MatchString(profile.CatalogID) {
			t.Fatalf("invalid or ambiguous collection profile: %+v", profile)
		}
		ids[profile.CatalogID] = true
		prefixes[profile.Prefix] = true
		if got := collectionSearchSpace(profile.Prefix+"2099-01.zim", int64(profile.SourceBytes)); got != profile.searchSpaceEstimate {
			t.Fatalf("collection profile not applied: %+v", profile)
		}
	}
}
func TestControlPanelUsesCoreSearchSpaceProfiles(t *testing.T) {
	a := &app{root: t.TempDir(), token: "test"}
	response := httptest.NewRecorder()
	a.handler().ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1:9999/", nil))
	if response.Code != 200 {
		t.Fatalf("panel status %d", response.Code)
	}
	body := response.Body.Bytes()
	if bytes.Contains(body, []byte("/*SEARCH_SPACE_PROFILES*/null")) || !bytes.Contains(body, searchSpaceJSON) {
		t.Fatal("panel did not receive core space profiles")
	}
	start := bytes.Index(body, []byte("const searchSpaceProfiles=")) + len("const searchSpaceProfiles=")
	end := bytes.Index(body[start:], []byte(";")) + start
	var config searchSpaceConfig
	if err := json.Unmarshal(body[start:end], &config); err != nil {
		t.Fatal(err)
	}
	if config.Families["ifixit"] != collectionSearchSpace("ifixit_test.zim", 0) {
		t.Fatal("core and UI differ")
	}
}

func TestSearchSpaceScalesUpForLargerArchives(t *testing.T) {
	profile := searchSpaceEstimate{SourceBytes: 100, PeakBytes: 1000, FinishedBytes: 100}
	for _, test := range []struct {
		source         int64
		peak, finished uint64
	}{{50, 1000, 100}, {100, 1000, 100}, {200, 2000, 200}} {
		got := scaleSearchSpace(profile, test.source)
		if got.PeakBytes != test.peak || got.FinishedBytes != test.finished {
			t.Fatalf("source %d: %+v", test.source, got)
		}
	}
}

func TestSearchSpaceScalingSaturates(t *testing.T) {
	if got := scaleSearchBytes(^uint64(0), 2); got != ^uint64(0) {
		t.Fatalf("overflowed estimate: %d", got)
	}
}

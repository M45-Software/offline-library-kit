package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStagingOptionsExcludeSameStorage(t *testing.T) {
	root := t.TempDir()
	options := stagingOptions(root, []driveOption{{root, "Current drive", 1000000000000}, {t.TempDir(), "Same filesystem", 1000000000000}})
	for _, option := range options {
		same, _, err := sharesStorage(root, option.Path)
		if err != nil || same {
			t.Fatalf("suggested unavailable or shared storage: %+v %v", option, err)
		}
		if option.Label == "Current drive" || option.Label == "Same filesystem" {
			t.Fatalf("suggested same filesystem: %+v", option)
		}
	}
}
func TestDriveResponseBindsStagingOptionsToLibrary(t *testing.T) {
	a := &app{root: t.TempDir()}
	mux := http.NewServeMux()
	a.addDriveHandler(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("GET", "/api/drives", nil))
	if response.Code != 200 {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		LibraryRoot    string          `json:"libraryRoot"`
		StagingOptions []stagingOption `json:"stagingOptions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.LibraryRoot != a.root || result.StagingOptions == nil {
		t.Fatalf("missing library context: %+v", result)
	}
}

package main

import (
	"encoding/json"
	"math"
	"strings"
)

type searchSpaceEstimate struct {
	SourceBytes   uint64 `json:"sourceBytes,omitempty"`
	PeakBytes     uint64 `json:"peakBytes"`
	FinishedBytes uint64 `json:"finishedBytes"`
}
type searchSpaceCollection struct {
	Prefix    string `json:"prefix"`
	CatalogID string `json:"catalogID"`
	searchSpaceEstimate
}
type searchSpaceConfig struct {
	Default     searchSpaceEstimate            `json:"default"`
	Families    map[string]searchSpaceEstimate `json:"families"`
	Collections []searchSpaceCollection        `json:"collections"`
}

// The core and control panel consume the same planning allowances, measured where available.
// These describe an index build's peak INCLUDING its finished index, excluding
// the archive and bundled packs. Unrecognized versions still need write guards.
var searchSpaceJSON, searchSpaceProfiles = loadSearchSpaceProfiles()

func loadSearchSpaceProfiles() ([]byte, searchSpaceConfig) {
	data, err := assets.ReadFile("search-space.json")
	if err != nil {
		panic(err)
	}
	var config searchSpaceConfig
	if err = json.Unmarshal(data, &config); err != nil {
		panic(err)
	}
	return data, config
}
func collectionSearchSpace(name string, sourceBytes int64) searchSpaceEstimate {
	name = strings.ToLower(name)
	for _, profile := range searchSpaceProfiles.Collections {
		if strings.HasPrefix(name, profile.Prefix) {
			return scaleSearchSpace(profile.searchSpaceEstimate, sourceBytes)
		}
	}
	family := ""
	switch {
	case isIFixit(name) || strings.HasSuffix(name, repairedSuffix):
		family = "ifixit"
	case strings.HasPrefix(name, "wikihow_"):
		family = "wikihow"
	case strings.HasPrefix(name, "gutenberg_"):
		family = "gutenberg"
	case strings.HasPrefix(name, "maps_"):
		family = "maps"
	}
	if profile, ok := searchSpaceProfiles.Families[family]; ok {
		return scaleSearchSpace(profile, sourceBytes)
	}
	return scaleSearchSpace(searchSpaceProfiles.Default, sourceBytes)
}
func scaleSearchSpace(profile searchSpaceEstimate, sourceBytes int64) searchSpaceEstimate {
	if sourceBytes > 0 && profile.SourceBytes > 0 && uint64(sourceBytes) > profile.SourceBytes {
		ratio := float64(sourceBytes) / float64(profile.SourceBytes)
		profile.PeakBytes = scaleSearchBytes(profile.PeakBytes, ratio)
		profile.FinishedBytes = scaleSearchBytes(profile.FinishedBytes, ratio)
	}
	return profile
}
func collectionSearchWorkAllowance(name string, sourceBytes int64) uint64 {
	return collectionSearchSpace(name, sourceBytes).PeakBytes
}

func scaleSearchBytes(value uint64, ratio float64) uint64 {
	scaled := math.Ceil(float64(value) * ratio)
	if scaled >= float64(^uint64(0)) {
		return ^uint64(0)
	}
	return uint64(scaled)
}

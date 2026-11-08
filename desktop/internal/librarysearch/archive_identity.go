package librarysearch

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type archiveIdentity struct {
	Collection, UUID string
	Bytes            int64
	ChecksumMD5      string `json:",omitempty"`
}

// Fixed header facts are documented by openZIM's reference implementation:
// https://github.com/openzim/libzim/blob/main/src/fileheader.cpp
// This reads identity metadata, not a full integrity check of the source archive.
func readArchiveIdentity(path, collection string) (id archiveIdentity, err error) {
	id.Collection = collection
	f, e := os.Open(path)
	if e != nil {
		return id, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return id, e
	}
	if !st.Mode().IsRegular() || st.Size() < 80 {
		return id, errors.New("short or nonregular ZIM archive")
	}
	var h [80]byte
	if _, e = io.ReadFull(f, h[:]); e != nil {
		return id, e
	}
	major := binary.LittleEndian.Uint16(h[4:])
	if binary.LittleEndian.Uint32(h[:]) != 0x044d495a || (major != 5 && major != 6) {
		return id, errors.New("unsupported ZIM header")
	}
	u := hex.EncodeToString(h[8:24])
	if u == strings.Repeat("0", 32) {
		return id, errors.New("ZIM archive has no usable UUID")
	}
	id.UUID = u[:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:]
	id.Bytes = st.Size()
	if binary.LittleEndian.Uint64(h[56:]) >= 80 {
		pos := binary.LittleEndian.Uint64(h[72:])
		if pos != 0 {
			if pos < 80 || pos > uint64(st.Size()-16) {
				return id, errors.New("invalid ZIM checksum offset")
			}
			var checksum [16]byte
			if _, e = f.ReadAt(checksum[:], int64(pos)); e != nil {
				return id, e
			}
			id.ChecksumMD5 = hex.EncodeToString(checksum[:])
		}
	}
	return id, nil
}
func identitiesInDirectory(dir string, collections map[string]string) ([]archiveIdentity, error) {
	names := make([]string, 0, len(collections))
	for name := range collections {
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") {
			return nil, errors.New("invalid archive collection")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var ids []archiveIdentity
	for _, name := range names {
		id, e := readArchiveIdentity(filepath.Join(dir, name+".zim"), name)
		if e != nil {
			return nil, fmt.Errorf("archive %s: %w", name, e)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
func compactSourceIdentities(queueRoot, expanded string, collections map[string]string) ([]archiveIdentity, error) {
	root := queueRoot
	if root == "" {
		root = expanded
	}
	if root == "" {
		return nil, nil
	}
	b, e := os.ReadFile(filepath.Join(root, "plan.json"))
	if e != nil {
		return nil, e
	}
	var p expandPlan
	if e = json.Unmarshal(b, &p); e != nil {
		return nil, e
	}
	byName := map[string]sourceQueue{}
	for _, q := range p.Queues {
		byName[q.Name] = q
	}
	names := make([]string, 0, len(collections))
	for name := range collections {
		names = append(names, name)
	}
	sort.Strings(names)
	var ids []archiveIdentity
	for _, name := range names {
		q, ok := byName[name]
		if !ok {
			return nil, errors.New("collection missing from source plan")
		}
		st, e := os.Stat(q.Source)
		if e != nil || st.Size() != q.SourceBytes || st.ModTime().UnixNano() != q.SourceMtime {
			return nil, fmt.Errorf("source archive changed: %s", name)
		}
		id, e := readArchiveIdentity(q.Source, name)
		if e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Matching ignores filenames and modification times so copied/renamed archives
// still match. The downloader must separately verify the archive's full checksum.
func matchArchiveIdentities(wanted []archiveIdentity, dir string) (map[string]string, []string, error) {
	if len(wanted) == 0 {
		return nil, nil, errors.New("index has no recorded source archive identities")
	}
	paths, e := filepath.Glob(filepath.Join(dir, "*.zim"))
	if e != nil {
		return nil, nil, e
	}
	byUUID := map[string][]archiveIdentity{}
	for _, path := range paths {
		id, e := readArchiveIdentity(path, strings.TrimSuffix(filepath.Base(path), ".zim"))
		if e != nil {
			continue
		}
		byUUID[id.UUID] = append(byUUID[id.UUID], id)
	}
	matched := map[string]string{}
	var missing []string
	for _, expected := range wanted {
		found := ""
		for _, actual := range byUUID[expected.UUID] {
			if actual.Bytes == expected.Bytes && actual.ChecksumMD5 == expected.ChecksumMD5 {
				found = actual.Collection
				break
			}
		}
		if found == "" {
			missing = append(missing, expected.Collection)
		} else {
			matched[expected.Collection] = found
		}
	}
	return matched, missing, nil
}
func bindCompactArchives(dir, sources string) error {
	path := filepath.Join(dir, "manifest.json")
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var m compactManifest
	if e = json.Unmarshal(b, &m); e != nil {
		return e
	}
	if m.Version != 4 {
		return errors.New("archive binding requires version 4")
	}
	ids, e := identitiesInDirectory(sources, m.Collections)
	if e != nil {
		return e
	}
	if len(m.Archives) > 0 {
		_, missing, e := matchArchiveIdentities(m.Archives, sources)
		if e != nil {
			return e
		}
		if len(missing) > 0 {
			return errors.New("source archives differ from the existing binding")
		}
	}
	m.Archives = ids
	return atomicJSON(path, m)
}

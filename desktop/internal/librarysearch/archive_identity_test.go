package librarysearch

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func writeTestArchive(t *testing.T, path string, seed byte) {
	t.Helper()
	b := make([]byte, 96)
	binary.LittleEndian.PutUint32(b, 0x044d495a)
	binary.LittleEndian.PutUint16(b[4:], 6)
	binary.LittleEndian.PutUint16(b[6:], 1)
	for i := 0; i < 16; i++ {
		b[8+i] = seed + byte(i) + 1
		b[80+i] = seed + byte(i) + 17
	}
	binary.LittleEndian.PutUint64(b[56:], 80)
	binary.LittleEndian.PutUint64(b[72:], 80)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestArchiveIdentityPortableAcrossRenameAndDetectsChangedEdition(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "original.zim")
	writeTestArchive(t, path, 0)
	id, e := readArchiveIdentity(path, "wanted")
	if e != nil {
		t.Fatal(e)
	}
	if id.UUID != "01020304-0506-0708-090a-0b0c0d0e0f10" || id.Bytes != 96 || id.ChecksumMD5 == "" {
		t.Fatalf("incorrect identity: %+v", id)
	}
	renamed := filepath.Join(root, "renamed.zim")
	if e = os.Rename(path, renamed); e != nil {
		t.Fatal(e)
	}
	matches, missing, e := matchArchiveIdentities([]archiveIdentity{id}, root)
	if e != nil || len(missing) != 0 || matches["wanted"] != "renamed" {
		t.Fatalf("copied archive mismatch: %v %v %v", matches, missing, e)
	}
	writeTestArchive(t, renamed, 1)
	_, missing, e = matchArchiveIdentities([]archiveIdentity{id}, root)
	if e != nil || len(missing) != 1 {
		t.Fatal("changed edition accepted")
	}
	writeTestArchive(t, renamed, 0)
	f, _ := os.OpenFile(renamed, os.O_RDWR, 0600)
	f.WriteAt([]byte{255}, 80)
	f.Close()
	_, missing, e = matchArchiveIdentities([]archiveIdentity{id}, root)
	if e != nil || len(missing) != 1 {
		t.Fatal("different checksum metadata accepted")
	}
}
func TestArchiveIdentityRejectsBadHeaderAndSupportsNoChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.zim")
	os.WriteFile(path, []byte("bad"), 0600)
	if _, e := readArchiveIdentity(path, "test"); e == nil {
		t.Fatal("short header accepted")
	}
	writeTestArchive(t, path, 0)
	f, _ := os.OpenFile(path, os.O_RDWR, 0600)
	var offset [8]byte
	binary.LittleEndian.PutUint64(offset[:], 10000)
	f.WriteAt(offset[:], 72)
	f.Close()
	if _, e := readArchiveIdentity(path, "test"); e == nil {
		t.Fatal("checksum out of bounds accepted")
	}
	writeTestArchive(t, path, 0)
	f, _ = os.OpenFile(path, os.O_RDWR, 0600)
	binary.LittleEndian.PutUint64(offset[:], 72)
	f.WriteAt(offset[:], 56)
	f.Close()
	id, e := readArchiveIdentity(path, "test")
	if e != nil || id.ChecksumMD5 != "" {
		t.Fatalf("absent checksum metadata: %+v %v", id, e)
	}
}

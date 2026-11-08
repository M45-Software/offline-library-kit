package repair

import (
	"bytes"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"os"
	"path/filepath"
	"testing"
)

func TestClusterDecodersAndExtendedOffsets(t *testing.T) {
	for _, kind := range []byte{1, 4, 5} {
		for _, width := range []int{4, 8} {
			raw := make([]byte, 3*width)
			put := func(at int, v uint64) {
				if width == 8 {
					le.PutUint64(raw[at:], v)
				} else {
					le.PutUint32(raw[at:], uint32(v))
				}
			}
			put(0, uint64(3*width))
			put(width, uint64(3*width+3))
			put(2*width, uint64(3*width+6))
			raw = append(raw, []byte("onetwo")...)
			var packed []byte
			switch kind {
			case 1:
				packed = raw
			case 4:
				var b bytes.Buffer
				w, e := xz.NewWriter(&b)
				if e != nil {
					t.Fatal(e)
				}
				w.Write(raw)
				w.Close()
				packed = b.Bytes()
			case 5:
				w, e := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
				if e != nil {
					t.Fatal(e)
				}
				packed = w.EncodeAll(raw, nil)
				w.Close()
			}
			flag := kind
			if width == 8 {
				flag |= 16
			}
			data := append(make([]byte, 80), flag)
			data = append(data, packed...)
			file := filepath.Join(t.TempDir(), "cluster")
			os.WriteFile(file, data, 0600)
			f, e := os.Open(file)
			if e != nil {
				t.Fatal(e)
			}
			a := &archive{f: f, header: make([]byte, 80), clusters: []uint64{80}, checksum: int64(len(data)), cached: ^uint32(0)}
			got, e := a.content(entry{cluster: 0, blob: 1})
			f.Close()
			if e != nil || string(got) != "two" {
				t.Fatalf("compression=%d width=%d: %q %v", kind, width, got, e)
			}
		}
	}
}

func TestIndexReaderAcceptsLongBookWithoutRelaxingRepairBound(t *testing.T) {
	const size = maxPage + 1
	data := make([]byte, 1+8+size)
	data[0] = 1
	le.PutUint32(data[1:], 8)
	le.PutUint32(data[5:], uint32(8+size))
	a := &archive{decoded: data, cached: 0}
	en := entry{cluster: 0, blob: 0}
	if _, err := a.content(en); err == nil {
		t.Fatal("repair accepted an oversized page")
	}
	got, err := a.contentWithLimit(en, maxCluster)
	if err != nil || len(got) != size {
		t.Fatalf("long book: %d %v", len(got), err)
	}
	// The indexing reader still enforces its configured page bound.
	if _, err = a.contentWithLimit(en, uint64(size-1)); err == nil {
		t.Fatal("index ignored its page bound")
	}
}

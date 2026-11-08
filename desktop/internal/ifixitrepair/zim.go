package repair

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"io"
	"os"
)

var le = binary.LittleEndian

const maxCluster = 128 << 20
const maxPage = 16 << 20

type entry struct {
	path          string
	ns            byte
	mime          uint16
	cluster, blob uint32
	offset        int64
	redirect      bool
}
type archive struct {
	f        *os.File
	header   []byte
	checksum int64
	clusters []uint64
	entries  []entry
	paths    map[string]int
	mimes    []string
	cached   uint32
	decoded  []byte
}

func readAt(f *os.File, p int64, n int) ([]byte, error) {
	if p < 0 || n < 0 {
		return nil, errors.New("invalid read")
	}
	b := make([]byte, n)
	_, e := f.ReadAt(b, p)
	return b, e
}
func openArchive(name string) (*archive, error) {
	return openArchiveContext(context.Background(), name)
}

func openArchiveContext(ctx context.Context, name string) (*archive, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, e := os.Open(name)
	if e != nil {
		return nil, e
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	h, e := readAt(f, 0, 80)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if le.Uint32(h) != 72173914 || le.Uint16(h[4:]) != 6 || le.Uint16(h[6:]) > 3 {
		return nil, errors.New("unsupported ZIM version; expected 6.0–6.3")
	}
	count, ncl := le.Uint32(h[24:]), le.Uint32(h[28:])
	sum := le.Uint64(h[72:])
	if count > 5000000 || ncl > 5000000 || sum > uint64(st.Size()) || sum+16 != uint64(st.Size()) {
		return nil, errors.New("invalid ZIM size or entry limits")
	}
	a := &archive{f: f, header: h, checksum: int64(sum), paths: make(map[string]int), cached: ^uint32(0)}
	urlpos, clpos, mimepos := le.Uint64(h[32:]), le.Uint64(h[48:]), le.Uint64(h[56:])
	if urlpos > sum || clpos > sum || uint64(count)*8 > sum-urlpos || uint64(ncl)*8 > sum-clpos || mimepos >= sum {
		return nil, errors.New("invalid pointer table bounds")
	}
	raw, e := readAt(f, int64(clpos), int(ncl)*8)
	if e != nil {
		return nil, e
	}
	for i := uint32(0); i < ncl; i++ {
		p := le.Uint64(raw[i*8:])
		if p < 80 || p >= sum || (i > 0 && p <= a.clusters[i-1]) {
			return nil, errors.New("invalid cluster pointers")
		}
		a.clusters = append(a.clusters, p)
	}
	r := bufio.NewReader(io.NewSectionReader(f, int64(mimepos), min(int64(1<<20), int64(sum-mimepos))))
	for {
		v, e := r.ReadString(0)
		if e != nil {
			return nil, e
		}
		if v == "\x00" {
			break
		}
		a.mimes = append(a.mimes, v[:len(v)-1])
	}
	pointers, e := readAt(f, int64(urlpos), int(count)*8)
	if e != nil {
		return nil, e
	}
	a.entries = make([]entry, count)
	for i := uint32(0); i < count; i++ {
		if i%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		p := le.Uint64(pointers[i*8:])
		if p < 80 || p+16 > sum {
			return nil, errors.New("invalid directory pointer")
		}
		d, e := readAt(f, int64(p), 16)
		if e != nil {
			return nil, e
		}
		mime := le.Uint16(d)
		redir := mime == 0xffff
		fixed := 16
		if redir {
			fixed = 12
			if le.Uint32(d[8:]) >= count {
				return nil, errors.New("invalid redirect target")
			}
		}
		if mime >= 0xfffe && !redir {
			return nil, errors.New("unsupported special directory entry")
		}
		if !redir && int(mime) >= len(a.mimes) {
			return nil, errors.New("invalid MIME index")
		}
		reader := bufio.NewReaderSize(io.NewSectionReader(f, int64(p)+int64(fixed), min(int64(8192), int64(sum-p)-int64(fixed))), 256)
		u, e := reader.ReadString(0)
		if e != nil {
			return nil, fmt.Errorf("entry %d path: %w", i, e)
		}
		u = u[:len(u)-1]
		en := entry{path: u, ns: d[3], mime: mime, offset: int64(p), redirect: redir}
		if !redir {
			en.cluster = le.Uint32(d[8:])
			en.blob = le.Uint32(d[12:])
			if int(en.cluster) >= len(a.clusters) {
				return nil, errors.New("invalid cluster reference")
			}
		}
		a.entries[i] = en
		if en.ns == 'C' || en.ns == 'A' {
			if _, exists := a.paths[u]; exists {
				return nil, errors.New("duplicate article path")
			}
			a.paths[u] = int(i)
		}
	}
	ok = true
	return a, nil
}
func (a *archive) verify() error {
	h := md5.New()
	if _, e := io.Copy(h, io.NewSectionReader(a.f, 0, a.checksum)); e != nil {
		return e
	}
	want, e := readAt(a.f, a.checksum, 16)
	if e != nil {
		return e
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return errors.New("source checksum does not match")
	}
	return nil
}
func (a *archive) content(en entry) ([]byte, error) {
	return a.contentWithLimit(en, maxPage)
}

func (a *archive) contentWithLimit(en entry, pageLimit uint64) ([]byte, error) {
	if en.redirect {
		return nil, errors.New("redirect has no blob")
	}
	if a.cached != en.cluster {
		start := a.clusters[en.cluster]
		end := uint64(a.checksum)
		if int(en.cluster)+1 < len(a.clusters) {
			end = a.clusters[en.cluster+1]
		}
		for _, p := range []uint64{le.Uint64(a.header[32:]), le.Uint64(a.header[40:]), le.Uint64(a.header[48:]), le.Uint64(a.header[56:])} {
			if p > start && p < end {
				end = p
			}
		}
		if end <= start || end-start > maxCluster {
			return nil, errors.New("compressed cluster exceeds bound")
		}
		raw, e := readAt(a.f, int64(start), int(end-start))
		if e != nil {
			return nil, e
		}
		kind := raw[0] & 15
		var r io.Reader = bytes.NewReader(raw[1:])
		var close func()
		switch kind {
		case 0, 1:
		case 4:
			r, e = (xz.ReaderConfig{DictCap: 64 << 20, SingleStream: true}).NewReader(r)
		case 5:
			var d *zstd.Decoder
			d, e = zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxCluster))
			if e == nil {
				r = d
				close = d.Close
			}
		default:
			return nil, fmt.Errorf("unsupported compression %d", kind)
		}
		if e != nil {
			return nil, e
		}
		data, e := io.ReadAll(io.LimitReader(r, maxCluster+1))
		if close != nil {
			close()
		}
		if e != nil {
			return nil, e
		}
		if len(data) > maxCluster {
			return nil, errors.New("decoded cluster exceeds bound")
		}
		a.decoded = append([]byte{raw[0]}, data...)
		a.cached = en.cluster
	}
	data := a.decoded[1:]
	width := 4
	if a.decoded[0]&16 != 0 {
		width = 8
	}
	get := func(pos int) (uint64, error) {
		if pos < 0 || pos+width > len(data) {
			return 0, errors.New("invalid blob offset")
		}
		if width == 8 {
			return le.Uint64(data[pos:]), nil
		}
		return uint64(le.Uint32(data[pos:])), nil
	}
	first, e := get(0)
	if e != nil || first < uint64(width) || first > uint64(len(data)) || first%uint64(width) != 0 {
		return nil, errors.New("invalid blob table")
	}
	if uint64(en.blob)+2 > first/uint64(width) {
		return nil, errors.New("blob outside table")
	}
	start, e := get(int(en.blob) * width)
	if e != nil {
		return nil, e
	}
	end, e := get((int(en.blob) + 1) * width)
	if e != nil || start < first || end < start || end > uint64(len(data)) || end-start > pageLimit {
		return nil, errors.New("invalid or oversized page")
	}
	return data[start:end], nil
}

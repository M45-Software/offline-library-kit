package librarysearch

// External merge sorting keeps construction independent of corpus size.
import (
	"bufio"
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type sortRecord struct {
	key   string
	value []byte
}
type diskSorter struct {
	guard               func() error
	dir                 string
	prefix              string
	limit, used, serial int
	rows                []sortRecord
	paths               []string
	ctx                 context.Context
}

func newDiskSorter(ctx context.Context, dir, prefix string, bytes int) *diskSorter {
	return &diskSorter{dir: dir, prefix: prefix, limit: bytes, ctx: ctx}
}
func (s *diskSorter) add(key string, value []byte) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.rows = append(s.rows, sortRecord{key, value})
	s.used += len(key) + len(value) + 64
	if s.used >= s.limit {
		return s.flush()
	}
	return nil
}
func writeSortRecord(w io.Writer, r sortRecord) error {
	if len(r.key) > 1<<20 || len(r.value) > 16<<20 {
		return errors.New("sort record exceeds bound")
	}
	var h [8]byte
	binary.LittleEndian.PutUint32(h[:4], uint32(len(r.key)))
	binary.LittleEndian.PutUint32(h[4:], uint32(len(r.value)))
	if _, e := w.Write(h[:]); e != nil {
		return e
	}
	if _, e := io.WriteString(w, r.key); e != nil {
		return e
	}
	_, e := w.Write(r.value)
	return e
}
func readSortRecord(r io.Reader) (sortRecord, error) {
	var h [8]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return sortRecord{}, e
	}
	k, v := binary.LittleEndian.Uint32(h[:4]), binary.LittleEndian.Uint32(h[4:])
	if k > 1<<20 || v > 16<<20 {
		return sortRecord{}, errors.New("invalid sort record")
	}
	b := make([]byte, int(k)+int(v))
	if _, e := io.ReadFull(r, b); e != nil {
		return sortRecord{}, e
	}
	return sortRecord{string(b[:k]), b[k:]}, nil
}
func (s *diskSorter) newPath() string {
	s.serial++
	return filepath.Join(s.dir, fmt.Sprintf("%s-%07d.run", s.prefix, s.serial))
}
func (s *diskSorter) flush() error {
	if s.guard != nil {
		if e := s.guard(); e != nil {
			return e
		}
	}
	if len(s.rows) == 0 {
		return nil
	}
	sort.Slice(s.rows, func(i, j int) bool { return s.rows[i].key < s.rows[j].key })
	path := s.newPath()
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	w := bufio.NewWriterSize(f, 256<<10)
	for _, r := range s.rows {
		if e = writeSortRecord(w, r); e != nil {
			f.Close()
			return e
		}
	}
	if e = w.Flush(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	s.paths = append(s.paths, path)
	s.rows = nil
	s.used = 0
	return nil
}

type mergeItem struct {
	row sortRecord
	run int
}
type mergeHeap []mergeItem

func (h mergeHeap) Len() int { return len(h) }
func (h mergeHeap) Less(i, j int) bool {
	if h[i].row.key == h[j].row.key {
		return h[i].run < h[j].run
	}
	return h[i].row.key < h[j].row.key
}
func (h mergeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x any)   { *h = append(*h, x.(mergeItem)) }
func (h *mergeHeap) Pop() any     { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }
func mergeRuns(ctx context.Context, paths []string, consume func(sortRecord) error) error {
	files := make([]*os.File, 0, len(paths))
	readers := make([]*bufio.Reader, 0, len(paths))
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	h := mergeHeap{}
	for i, p := range paths {
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		files = append(files, f)
		r := bufio.NewReaderSize(f, 64<<10)
		readers = append(readers, r)
		row, e := readSortRecord(r)
		if e == io.EOF {
			continue
		}
		if e != nil {
			return e
		}
		heap.Push(&h, mergeItem{row, i})
	}
	for len(h) > 0 {
		if e := ctx.Err(); e != nil {
			return e
		}
		item := heap.Pop(&h).(mergeItem)
		if e := consume(item.row); e != nil {
			return e
		}
		row, e := readSortRecord(readers[item.run])
		if e == nil {
			heap.Push(&h, mergeItem{row, item.run})
		} else if e != io.EOF {
			return e
		}
	}
	return nil
}
func (s *diskSorter) finish(consume func(sortRecord) error) error {
	if e := s.flush(); e != nil {
		return e
	}
	// Bounded fan-in avoids holding thousands of source files open.
	for len(s.paths) > 24 {
		var next []string
		for start := 0; start < len(s.paths); start += 24 {
			end := start + 24
			if end > len(s.paths) {
				end = len(s.paths)
			}
			path := s.newPath()
			f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			w := bufio.NewWriterSize(f, 256<<10)
			written := 0
			e = mergeRuns(s.ctx, s.paths[start:end], func(r sortRecord) error {
				written += len(r.key) + len(r.value) + 8
				if written >= 32<<20 && s.guard != nil {
					if err := s.guard(); err != nil {
						return err
					}
					written = 0
				}
				return writeSortRecord(w, r)
			})
			if e == nil {
				e = w.Flush()
			}
			ce := f.Close()
			if e == nil {
				e = ce
			}
			if e != nil {
				return e
			}
			next = append(next, path)
			for _, p := range s.paths[start:end] {
				if e = os.Remove(p); e != nil {
					return e
				}
			}
		}
		s.paths = next
	}
	if e := mergeRuns(s.ctx, s.paths, consume); e != nil {
		return e
	}
	for _, p := range s.paths {
		if e := os.Remove(p); e != nil {
			return e
		}
	}
	s.paths = nil
	return nil
}

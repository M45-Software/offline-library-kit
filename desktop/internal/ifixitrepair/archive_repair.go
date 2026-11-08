package repair

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/klauspost/compress/zstd"
	"io"
	"os"
	"sort"
)

type Report struct {
	Source                  string `json:"source"`
	Output                  string `json:"output"`
	Pages                   int    `json:"pages_checked"`
	ChangedPages            int    `json:"changed_pages"`
	Links                   int    `json:"links_repaired"`
	Notices                 int    `json:"coverage_notices"`
	OriginalIndexRetained   bool   `json:"original_search_index_retained"`
	MissingContentRecovered bool   `json:"missing_content_recovered"`
	Checksum                string `json:"output_md5"`
}

func run(ctx context.Context, source, destination string) (Report, error) {
	return runWithWorkers(ctx, source, destination, 1)
}

func runWithWorkers(ctx context.Context, source, destination string, workers int) (Report, error) {
	return runWithOptions(ctx, source, destination, Options{Workers: workers})
}

func runWithOptions(ctx context.Context, source, destination string, options Options) (Report, error) {
	workers := options.Workers
	progress := func(phase string, done, total int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if options.Progress != nil {
			return options.Progress(Progress{phase, done, total})
		}
		return nil
	}
	if err := progress("reading", 0, 0); err != nil {
		return Report{}, err
	}
	rep := Report{Source: source, Output: destination, OriginalIndexRetained: true}
	if workers < 1 || workers > 4 {
		return rep, errors.New("workers must be between 1 and 4")
	}
	a, e := openArchiveContext(ctx, source)
	if e != nil {
		return rep, e
	}
	defer a.f.Close()
	before, e := a.f.Stat()
	if e != nil {
		return rep, e
	}
	file, e := os.OpenFile(destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return rep, e
	}
	defer file.Close()
	out := &guardedOutput{File: file, beforeWrite: options.BeforeWrite}
	// An interrupted output is intentionally retained, never overwritten on retry.
	if e = progress("copying", 0, a.checksum); e != nil {
		return rep, e
	}
	h := md5.New()
	if _, e = copyWithProgress(ctx, io.MultiWriter(out, h), io.NewSectionReader(a.f, 0, a.checksum), func(done int64) error { return progress("copying", done, a.checksum) }); e != nil {
		return rep, e
	}
	sum, e := readAt(a.f, a.checksum, 16)
	if e != nil {
		return rep, e
	}
	if hex.EncodeToString(h.Sum(nil)) != hex.EncodeToString(sum) {
		return rep, errors.New("source checksum does not match; incomplete output retained")
	}
	offsets := append([]uint64(nil), a.clusters...)
	encoder, e := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault))
	if e != nil {
		return rep, e
	}
	defer encoder.Close()
	pending := [][]byte{}
	pendingBytes := 0
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		position, e := out.Seek(0, io.SeekCurrent)
		if e != nil {
			return e
		}
		offsets = append(offsets, uint64(position))
		table := make([]byte, (len(pending)+1)*4)
		pos := len(table)
		for i, b := range pending {
			le.PutUint32(table[i*4:], uint32(pos))
			pos += len(b)
		}
		le.PutUint32(table[len(pending)*4:], uint32(pos))
		raw := make([]byte, 0, pos)
		raw = append(raw, table...)
		for _, b := range pending {
			raw = append(raw, b...)
		}
		compressed := encoder.EncodeAll(raw, nil)
		if _, e = out.Write([]byte{5}); e == nil {
			_, e = out.Write(compressed)
		}
		pending = nil
		pendingBytes = 0
		return e
	}
	r := newResolver(a.paths)
	indexes := []int{}
	for i, en := range a.entries {
		if !en.redirect && (en.ns == 'C' || en.ns == 'A') && a.mimes[en.mime] == "text/html" {
			indexes = append(indexes, i)
		}
	}
	sort.Slice(indexes, func(i, j int) bool {
		x, y := a.entries[indexes[i]], a.entries[indexes[j]]
		if x.cluster != y.cluster {
			return x.cluster < y.cluster
		}
		return x.blob < y.blob
	})
	if e = progress("pages", 0, int64(len(indexes))); e != nil {
		return rep, e
	}
	e = repairPages(ctx, a, indexes, r, workers, func(result pageResult) error {
		en, fixed, links, notice := result.en, result.data, result.links, result.notice
		rep.Pages++
		if links > 0 || notice {
			if pendingBytes+len(fixed) > 2<<20 {
				if e = flush(); e != nil {
					return e
				}
			}
			cluster := uint32(len(offsets))
			blob := uint32(len(pending))
			b := make([]byte, 8)
			le.PutUint32(b, cluster)
			le.PutUint32(b[4:], blob)
			if _, e = out.WriteAt(b, en.offset+8); e != nil {
				return e
			}
			pending = append(pending, append([]byte(nil), fixed...))
			pendingBytes += len(fixed)
			rep.ChangedPages++
			rep.Links += links
			if notice {
				rep.Notices++
			}
		}
		if rep.Pages%128 == 0 || rep.Pages == len(indexes) {
			if err := progress("pages", int64(rep.Pages), int64(len(indexes))); err != nil {
				return err
			}
		}
		return nil
	})
	if e != nil {
		return rep, e
	}
	if e = flush(); e != nil {
		return rep, e
	}
	tablePos, e := out.Seek(0, io.SeekCurrent)
	if e != nil {
		return rep, e
	}
	for _, pos := range offsets {
		b := make([]byte, 8)
		le.PutUint64(b, pos)
		if _, e = out.Write(b); e != nil {
			return rep, e
		}
	}
	checksumPos, e := out.Seek(0, io.SeekCurrent)
	if e != nil {
		return rep, e
	}
	le.PutUint32(a.header[28:], uint32(len(offsets)))
	le.PutUint64(a.header[48:], uint64(tablePos))
	le.PutUint64(a.header[72:], uint64(checksumPos))
	if _, e = rand.Read(a.header[8:24]); e != nil {
		return rep, e
	}
	if _, e = out.WriteAt(a.header, 0); e != nil {
		return rep, e
	}
	after, e := a.f.Stat()
	current, pathErr := os.Stat(source)
	if e != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(before, current) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return rep, errors.New("source changed during repair")
	}
	if e = progress("checksum", 0, checksumPos); e != nil {
		return rep, e
	}
	checksum := md5.New()
	if _, e = copyWithProgress(ctx, checksum, io.NewSectionReader(file, 0, checksumPos), func(done int64) error { return progress("checksum", done, checksumPos) }); e != nil {
		return rep, e
	}
	digest := checksum.Sum(nil)
	if _, e = out.Write(digest); e != nil {
		return rep, e
	}
	if e = out.Sync(); e != nil {
		return rep, e
	}
	rep.Checksum = hex.EncodeToString(digest)
	return rep, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(b)
}

// Options controls bounded HTML parallelism. Zero Workers selects two workers.
type Progress struct {
	Phase       string
	Done, Total int64
}

// Callbacks run serially on the writing goroutine. Returning an error stops
// repair and retains the partial output. BeforeWrite runs before every write.
type Options struct {
	Workers     int
	Progress    func(Progress) error
	BeforeWrite func(int) error
}

type guardedOutput struct {
	*os.File
	beforeWrite func(int) error
}

func (f *guardedOutput) Write(p []byte) (int, error) {
	if f.beforeWrite != nil {
		if err := f.beforeWrite(len(p)); err != nil {
			return 0, err
		}
	}
	return f.File.Write(p)
}
func (f *guardedOutput) WriteAt(p []byte, at int64) (int, error) {
	if f.beforeWrite != nil {
		if err := f.beforeWrite(len(p)); err != nil {
			return 0, err
		}
	}
	return f.File.WriteAt(p, at)
}
func copyWithProgress(ctx context.Context, w io.Writer, r io.Reader, progress func(int64) error) (int64, error) {
	var done int64
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		n, err := r.Read(buf)
		if n > 0 {
			written, writeErr := w.Write(buf[:n])
			done += int64(written)
			if writeErr != nil {
				return done, writeErr
			}
			if written != n {
				return done, io.ErrShortWrite
			}
			if err := progress(done); err != nil {
				return done, err
			}
		}
		if err == io.EOF {
			return done, nil
		}
		if err != nil {
			return done, err
		}
	}
}

// Repair writes a new locally repaired archive. It never replaces an existing
// output or modifies the original. A failed output is retained for inspection.
func Repair(ctx context.Context, source, output string, options Options) (Report, error) {
	workers := options.Workers
	if workers == 0 {
		workers = 2
	}
	options.Workers = workers
	return runWithOptions(ctx, source, output, options)
}

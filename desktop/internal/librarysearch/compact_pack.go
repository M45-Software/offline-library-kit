package librarysearch

import (
	"context"
	"crypto/sha256"
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

const maxCompactParts = 128

// Parts describe a logical index.bin. Byte offsets in the binary never change.
type compactPart struct {
	Name   string
	Bytes  int64
	SHA256 string
}
type compactStore struct {
	files []*os.File
	ends  []int64
	size  int64
}

func (s *compactStore) Close() error {
	var first error
	for _, f := range s.files {
		if e := f.Close(); e != nil && first == nil {
			first = e
		}
	}
	return first
}
func (s *compactStore) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative index offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= s.size {
		return 0, io.EOF
	}
	total := 0
	for len(p) > 0 && off < s.size {
		i := sort.Search(len(s.ends), func(i int) bool { return s.ends[i] > off })
		start := int64(0)
		if i > 0 {
			start = s.ends[i-1]
		}
		want := len(p)
		if int64(want) > s.ends[i]-off {
			want = int(s.ends[i] - off)
		}
		n, e := s.files[i].ReadAt(p[:want], off-start)
		total += n
		off += int64(n)
		p = p[n:]
		if e != nil {
			return total, e
		}
		if n != want {
			return total, io.ErrUnexpectedEOF
		}
	}
	if len(p) > 0 {
		return total, io.EOF
	}
	return total, nil
}
func openCompactStore(dir string, m compactManifest) (store *compactStore, err error) {
	store = &compactStore{}
	defer func() {
		if err != nil {
			store.Close()
		}
	}()
	parts := m.Parts
	if len(parts) == 0 {
		parts = []compactPart{{Name: "index.bin", Bytes: -1}}
	} else if len(parts) > maxCompactParts {
		return store, errors.New("too many index parts")
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if p.Name == "" || p.Name == "." || p.Name == ".." || filepath.Base(p.Name) != p.Name || strings.ContainsAny(p.Name, "/\\\x00") || seen[p.Name] {
			return store, errors.New("invalid or duplicate index part")
		}
		seen[p.Name] = true
		if len(m.Parts) > 0 {
			hash, e := hex.DecodeString(p.SHA256)
			if e != nil || len(hash) != 32 || p.Bytes <= 0 {
				return store, errors.New("invalid index part size or checksum")
			}
		}
		f, e := os.Open(filepath.Join(dir, p.Name))
		if e != nil {
			return store, e
		}
		store.files = append(store.files, f)
		st, e := f.Stat()
		if e != nil {
			return store, e
		}
		if !st.Mode().IsRegular() || st.Size() <= 0 || p.Bytes >= 0 && st.Size() != p.Bytes {
			return store, errors.New("missing, empty or changed index part")
		}
		if st.Size() > int64(^uint64(0)>>1)-store.size {
			return store, errors.New("index size overflow")
		}
		store.size += st.Size()
		store.ends = append(store.ends, store.size)
	}
	if len(m.Parts) > 0 && m.Stats.IndexBytes != store.size {
		return store, errors.New("multipart index total size mismatch")
	}
	return store, nil
}
func packCompact(ctx context.Context, source, out string, partBytes int64, guard func() error) (result compactManifest, err error) {
	if out == "" {
		return result, errors.New("provide a pack output directory")
	}
	if partBytes < 1 || partBytes > 16<<30 {
		return result, errors.New("part size must be 1 byte to 16 GiB")
	}
	if guard != nil {
		if e := guard(); e != nil {
			return result, e
		}
	}
	if _, e := os.Lstat(out); !os.IsNotExist(e) {
		return result, errors.New("pack output must be new")
	}
	data, e := os.ReadFile(filepath.Join(source, "manifest.json"))
	if e != nil {
		return result, e
	}
	if e = json.Unmarshal(data, &result); e != nil {
		return result, e
	}
	en, e := openCompact(source, 0)
	if e != nil {
		return result, e
	}
	defer en.close()
	count := (int64(en.size) + partBytes - 1) / partBytes
	if count > maxCompactParts {
		return result, errors.New("too many parts; increase part size")
	}
	temp, e := os.MkdirTemp(filepath.Dir(out), "."+filepath.Base(out)+"-partial-")
	if e != nil {
		return result, e
	}
	defer func() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "Incomplete pack retained:", temp)
		}
	}()
	full := sha256.New()
	result.Parts = nil
	var offset int64
	buffer := make([]byte, 256<<10)
	for n := int64(0); n < count; n++ {
		if e = ctx.Err(); e != nil {
			return result, e
		}
		if guard != nil {
			if e = guard(); e != nil {
				return result, e
			}
		}
		size := partBytes
		if size > int64(en.size)-offset {
			size = int64(en.size) - offset
		}
		name := fmt.Sprintf("index.part-%04d.bin", n+1)
		f, e := os.OpenFile(filepath.Join(temp, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return result, e
		}
		hash := sha256.New()
		reader := io.NewSectionReader(en.f, offset, size)
		writer := io.MultiWriter(f, hash, full)
		var written int64
		for written < size {
			if e = ctx.Err(); e != nil {
				f.Close()
				return result, e
			}
			chunk := int64(len(buffer))
			if chunk > size-written {
				chunk = size - written
			}
			read, re := reader.Read(buffer[:chunk])
			if read > 0 {
				if _, e = writer.Write(buffer[:read]); e != nil {
					f.Close()
					return result, e
				}
				written += int64(read)
			}
			if re != nil {
				if re == io.EOF && written == size {
					break
				}
				f.Close()
				return result, re
			}
			if guard != nil && written%(32<<20) == 0 {
				if e = guard(); e != nil {
					f.Close()
					return result, e
				}
			}
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return result, e
		}
		if e = f.Close(); e != nil {
			return result, e
		}
		result.Parts = append(result.Parts, compactPart{name, size, hex.EncodeToString(hash.Sum(nil))})
		offset += size
	}
	sum := hex.EncodeToString(full.Sum(nil))
	if result.IndexSHA256 == "" || sum != result.IndexSHA256 {
		return result, errors.New("source index SHA-256 mismatch")
	}
	if e = atomicJSON(filepath.Join(temp, "manifest.json"), result); e != nil {
		return result, e
	}
	if guard != nil {
		if e = guard(); e != nil {
			return result, e
		}
	}
	if _, e = os.Lstat(out); !os.IsNotExist(e) {
		return result, errors.New("pack output appeared during creation")
	}
	if e = os.Rename(temp, out); e != nil {
		return result, e
	}
	return result, nil
}
func verifyCompact(ctx context.Context, dir string) error {
	b, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		return e
	}
	var m compactManifest
	if e = json.Unmarshal(b, &m); e != nil {
		return e
	}
	en, e := openCompact(dir, 0)
	if e != nil {
		return e
	}
	defer en.close()
	full := sha256.New()
	buffer := make([]byte, 256<<10)
	for i, f := range en.f.files {
		if _, e = f.Seek(0, 0); e != nil {
			return e
		}
		hash := sha256.New()
		for {
			if e = ctx.Err(); e != nil {
				return e
			}
			n, re := f.Read(buffer)
			if n > 0 {
				full.Write(buffer[:n])
				hash.Write(buffer[:n])
			}
			if re == io.EOF {
				break
			}
			if re != nil {
				return re
			}
		}
		if len(m.Parts) > 0 && hex.EncodeToString(hash.Sum(nil)) != m.Parts[i].SHA256 {
			return fmt.Errorf("index part checksum mismatch: %s", m.Parts[i].Name)
		}
	}
	if m.IndexSHA256 == "" || hex.EncodeToString(full.Sum(nil)) != m.IndexSHA256 {
		return errors.New("logical index SHA-256 mismatch")
	}
	return nil
}

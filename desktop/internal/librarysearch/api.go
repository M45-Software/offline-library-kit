package librarysearch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Document = doc
type Identity = archiveIdentity
type Part = compactPart
type Manifest = compactManifest

func Identify(path string) (Identity, error) { return readArchiveIdentity(path, "") }
func ReadManifest(dir string) (Manifest, error) {
	var m Manifest
	f, err := os.Open(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&m)
	if err == nil && m.Version != 4 {
		err = errors.New("unsupported search index")
	}
	return m, err
}
func Verify(ctx context.Context, dir string) error { return verifyCompact(ctx, dir) }

// Library owns a single bounded payload cache across all open index packs.
type Library struct {
	mu    sync.Mutex
	cache *cache
	packs map[string]*compactEngine
}

func NewLibrary() *Library {
	return &Library{cache: newCache(64 << 20), packs: map[string]*compactEngine{}}
}
func (l *Library) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.packs {
		p.close()
	}
	l.packs = map[string]*compactEngine{}
}
func (l *Library) Query(ctx context.Context, dir string, id Identity, q, field string) (int, []Document, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, err := ReadManifest(dir)
	if os.IsNotExist(err) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	collection := ""
	for _, bound := range m.Archives {
		if bound.UUID == id.UUID && bound.Bytes == id.Bytes && bound.ChecksumMD5 == id.ChecksumMD5 {
			collection = bound.Collection
			break
		}
	}
	if collection == "" {
		return 0, nil, false, nil
	}
	en := l.packs[dir]
	if en == nil {
		if err = Verify(ctx, dir); err != nil {
			return 0, nil, true, err
		}
		en, err = openCompact(dir, 0)
		if err != nil {
			return 0, nil, true, err
		}
		en.cache = l.cache
		l.packs[dir] = en
	}
	if err = ctx.Err(); err != nil {
		return 0, nil, true, err
	}
	n, docs, err := en.queryFiltered(q, field, collection)
	return n, docs, true, err
}

// Build creates a new custom-only pack from streamed HTML passages. Successful
// builds remove their temporary text spool; failures retain it for inspection.
func Build(ctx context.Context, output string, id Identity, label string, produce func(func(Document) error) error, guard func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard != nil {
		if err := guard(); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp(filepath.Dir(output), ".search-source-*.part")
	if err != nil {
		return err
	}
	spool := f.Name()
	w := bufio.NewWriterSize(f, 256<<10)
	encoder := json.NewEncoder(w)
	err = produce(func(d Document) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if guard != nil {
			if err := guard(); err != nil {
				return err
			}
		}
		d.Collection = id.Collection
		d.CollectionLabel = label
		return encoder.Encode(d)
	})
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	_, err = buildCompact(ctx, spool, "", output, compactOptions{BodyWords: 0, SortMiB: 16, guard: guard})
	if err != nil {
		return err
	}
	m, err := ReadManifest(output)
	if err != nil {
		return err
	}
	m.Archives = []Identity{id}
	if err = atomicJSON(filepath.Join(output, "manifest.json"), m); err != nil {
		return err
	}
	if err = Verify(ctx, output); err != nil {
		return err
	}
	return os.Remove(spool)
}
func Extract(path, title string, raw []byte) ([]Document, error) {
	return passages(Document{Path: path, Title: title}, raw, 1000, 20000)
}

type NativeCollection struct{ UUID, Title, Language string }

func NativeSearch(ctx context.Context, reader, q string, collections []NativeCollection) (int, []Document, int, error) {
	n, err := newNativeClient(reader)
	if err != nil {
		return 0, nil, 0, err
	}
	books, err := n.inventory(ctx)
	if err != nil {
		return 0, nil, 0, err
	}
	groups := map[string][]collectionRoute{}
	var failures []string
	for _, c := range collections {
		if b, ok := books[c.UUID]; !ok || !b.FullText {
			failures = append(failures, c.Title)
			continue
		}
		language := c.Language
		if language == "" {
			language = "und:" + c.UUID
		}
		groups[language] = append(groups[language], collectionRoute{Collection: c.UUID, UUID: c.UUID, Label: c.Title, Language: language, Backend: "native"})
	}
	total, searched := 0, 0
	var results []Document
	for _, group := range groups {
		count, docs, e := n.search(ctx, q, group, books)
		if e != nil {
			failures = append(failures, e.Error())
			continue
		}
		total += count
		searched += len(group)
		results = append(results, docs...)
	}
	if len(failures) > 0 {
		return total, results, searched, errors.New(strings.Join(failures, "; "))
	}
	return total, results, searched, nil
}

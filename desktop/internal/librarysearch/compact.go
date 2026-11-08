package librarysearch

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const compactMagic = "ZSIDX004"
const compactHeaderBytes = 128
const compactBlockBytes = 32768
const maxCompactDirectory = 16 << 20
const (
	fieldTitle byte = 1 << iota
	fieldAuthor
	fieldSubject
	fieldHeading
	fieldBody
)

var compactCRC = crc32.MakeTable(crc32.Castagnoli)
var compactPool = sync.Pool{New: func() any { w, _ := flate.NewWriter(io.Discard, flate.DefaultCompression); return w }}

type compactOptions struct {
	Collections         map[string]bool
	QueueRoot, Metadata string
	Collection          string
	BodyWords           int
	SortMiB             int
	guard               func() error
}
type compactTerm struct {
	Word       string
	Off, Bytes uint64
	Count      uint32
	Inline     []byte `json:",omitempty"`
}
type compactDictionaryBlock struct {
	First   string
	FirstID uint32
	Loc     loc
}
type compactDirectory struct {
	Words []compactDictionaryBlock
	Pages []block
}
type compactStats struct {
	InputPassages                                        int64
	Pages, Terms                                         uint32
	BodyWords                                            int
	DictionaryBytes, PostingBytes, PageBytes, IndexBytes int64
	BuildSeconds                                         float64
}
type compactManifest struct {
	Archives    []archiveIdentity `json:",omitempty"`
	Parts       []compactPart     `json:",omitempty"`
	SourceType  string            `json:",omitempty"`
	IndexSHA256 string
	Ranges      map[string]compactRange
	Version     int
	Documents   int
	Shards      []string
	Codec       string
	BlockBytes  int
	Collections map[string]string
	BodyWords   int
	Stats       compactStats
	Semantics   string
}

func compactPack(raw []byte) []byte {
	if len(raw) < 96 {
		return append([]byte{0}, raw...)
	}
	var b bytes.Buffer
	w := compactPool.Get().(*flate.Writer)
	w.Reset(&b)
	w.Write(raw)
	w.Close()
	compactPool.Put(w)
	if b.Len() >= len(raw) {
		return append([]byte{0}, raw...)
	}
	return append([]byte{1}, b.Bytes()...)
}
func writeCompactBlock(f *os.File, raw []byte, count uint32) (loc, error) {
	if len(raw) > 16<<20 {
		return loc{}, errors.New("compact block too large")
	}
	p := compactPack(raw)
	off, e := f.Seek(0, io.SeekCurrent)
	if e != nil {
		return loc{}, e
	}
	_, e = f.Write(p)
	return loc{uint64(off), uint32(len(p)), uint32(len(raw)), crc32.Checksum(p, compactCRC), count}, e
}
func maskForField(field string) (byte, error) {
	switch field {
	case "title":
		return fieldTitle, nil
	case "author":
		return fieldAuthor, nil
	case "subject":
		return fieldSubject, nil
	case "heading":
		return fieldHeading, nil
	case "text":
		return fieldBody, nil
	case "all":
		return fieldTitle | fieldAuthor | fieldSubject, nil
	case "deep":
		return 31, nil
	}
	return 0, errors.New("invalid search field")
}
func pageKey(d doc) string { return d.Collection + "\x00" + d.Path }
func validateCompactDoc(d doc) error {
	if d.Collection == "" || strings.ContainsAny(d.Collection, "/\\\x00") || d.Path == "" || strings.ContainsRune(d.Path, 0) {
		return errors.New("invalid compact page identity")
	}
	for _, p := range strings.Split(d.Path, "/") {
		if p == ".." || p == "." {
			return errors.New("invalid compact entry path")
		}
	}
	if len(d.Authors) > 256 || len(d.Subjects) > 256 {
		return errors.New("metadata list too large")
	}
	if d.Passage < 0 || d.Passage > 1000000 {
		return errors.New("invalid passage number")
	}
	return nil
}

// walkCompactSource streams legacy records without loading their term dictionaries.
func walkCompactSource(input, expanded string, visit func(doc) error) error {
	return walkCompactSourceSelected(input, expanded, "", visit)
}
func walkCompactSourceSelected(input, expanded, collection string, visit func(doc) error) error {
	return walkCompactSourceFiltered(input, expanded, collection, nil, visit)
}
func walkCompactSourceFiltered(input, expanded, collection string, allowed map[string]bool, visit func(doc) error) error {
	if (input == "") == (expanded == "") {
		return errors.New("provide exactly one of -input or -expanded-root")
	}
	if input != "" {
		f, e := os.Open(input)
		if e != nil {
			return e
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 65536), 4<<20)
		for s.Scan() {
			var d doc
			if e = json.Unmarshal(s.Bytes(), &d); e != nil {
				return e
			}
			if allowed != nil && !allowed[d.Collection] {
				continue
			}
			if collection != "" && d.Collection != collection {
				continue
			}
			if e = visit(d); e != nil {
				return e
			}
		}
		return s.Err()
	}
	dirs, e := filepath.Glob(filepath.Join(expanded, "collections", "*"))
	if e != nil {
		return e
	}
	if len(dirs) == 0 {
		return errors.New("no committed source collections")
	}
	for _, dir := range dirs {
		if allowed != nil && !allowed[filepath.Base(dir)] {
			continue
		}
		if collection != "" && filepath.Base(dir) != collection {
			continue
		}
		state, e := loadExpandState(dir)
		if e != nil {
			return e
		}
		if state.Batch == 0 {
			continue
		}
		for n := 1; n <= state.Batch; n++ {
			batch := filepath.Join(dir, fmt.Sprintf("batch-%06d", n))
			b, e := os.ReadFile(filepath.Join(batch, "commit.json"))
			if e != nil {
				return e
			}
			var commit batchCommit
			if e = json.Unmarshal(b, &commit); e != nil {
				return e
			}
			for _, sh := range commit.Shards {
				if filepath.Base(sh.Name) != sh.Name {
					return errors.New("invalid source shard")
				}
				if e = walkLegacyShard(filepath.Join(batch, sh.Name), visit); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func walkLegacyShard(path string, visit func(doc) error) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	var h [64]byte
	if _, e = io.ReadFull(f, h[:]); e != nil {
		return e
	}
	version := 0
	switch string(h[:8]) {
	case magic:
		version = 1
	case magicV2:
		version = 2
	case magicV3:
		version = 3
	}
	if version == 0 {
		return errors.New("unsupported source shard")
	}
	off := binary.LittleEndian.Uint64(h[16:])
	if off > uint64(st.Size()) || uint64(st.Size())-off > 32<<20 {
		return errors.New("invalid source block directory")
	}
	if _, e = f.Seek(int64(off), 0); e != nil {
		return e
	}
	var n uint32
	if e = binary.Read(f, binary.LittleEndian, &n); e != nil {
		return e
	}
	if uint64(n)*28+4 != uint64(st.Size())-off {
		return errors.New("invalid source block count")
	}
	blocks := make([]block, n)
	if e = binary.Read(f, binary.LittleEndian, blocks); e != nil {
		return e
	}
	// Expanded commits use adaptive encoding. The manifest is checked per batch.
	b, e := os.ReadFile(filepath.Join(filepath.Dir(path), "manifest.json"))
	if e != nil {
		return e
	}
	var m manifest
	if e = json.Unmarshal(b, &m); e != nil {
		return e
	}
	if m.Codec != "adaptive" && m.Codec != "flate" && m.Codec != "none" {
		return errors.New("unsupported source codec")
	}
	var count uint32
	for _, bl := range blocks {
		if bl.First != count || bl.Loc.Count == 0 {
			return errors.New("source blocks are not contiguous")
		}
		if bl.Loc.N > 16<<20 || bl.Loc.Off+uint64(bl.Loc.N) > off {
			return errors.New("invalid source data block")
		}
		p := make([]byte, bl.Loc.N)
		if _, e = f.ReadAt(p, int64(bl.Loc.Off)); e != nil {
			return e
		}
		raw, e := unpack(p, bl.Loc, m.Codec)
		if e != nil {
			return e
		}
		r := bytes.NewReader(raw)
		for j := uint32(0); j < bl.Loc.Count; j++ {
			d, e := decodeDocVersion(r, version)
			if e != nil {
				return e
			}
			if e = visit(d); e != nil {
				return e
			}
			count++
		}
		if r.Len() != 0 {
			return errors.New("source document block has trailing bytes")
		}
	}
	if count != binary.LittleEndian.Uint32(h[24:]) {
		return errors.New("source document count mismatch")
	}
	return nil
}
func buildCompact(ctx context.Context, input, expanded, out string, options compactOptions) (stats compactStats, err error) {
	started := time.Now()
	if options.QueueRoot != "" {
		if input != "" || expanded != "" {
			return stats, errors.New("catalog queues cannot be combined with other inputs")
		}
		options.BodyWords = -1
	}
	if options.BodyWords < -1 || options.BodyWords > 1000000 || options.SortMiB < 1 || options.SortMiB > 128 {
		return stats, errors.New("body words must be -1 (none), 0 (all), or 1-1000000; sort memory must be 1-128 MiB")
	}
	if options.guard != nil {
		if e := options.guard(); e != nil {
			return stats, e
		}
	}
	if _, e := os.Lstat(out); !os.IsNotExist(e) {
		return stats, errors.New("output must be a new directory")
	}
	temp, e := os.MkdirTemp(filepath.Dir(out), "."+filepath.Base(out)+"-partial-")
	if e != nil {
		return stats, e
	}
	// Keep an interrupted construction for diagnosis; only a final manifest is usable.
	defer func() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "Incomplete compact build retained:", temp)
		}
	}()
	sortdir := filepath.Join(temp, "work")
	if e = os.Mkdir(sortdir, 0700); e != nil {
		return stats, e
	}
	docs := newDiskSorter(ctx, sortdir, "pages", options.SortMiB<<20)
	docs.guard = options.guard
	visit := func(d doc) error {
		if e := validateCompactDoc(d); e != nil {
			return e
		}
		b, e := json.Marshal(d)
		if e != nil {
			return e
		}
		key := pageKey(d) + "\x00" + fmt.Sprintf("%07d", d.Passage)
		stats.InputPassages++
		if stats.InputPassages%100000 == 0 {
			fmt.Fprintf(os.Stderr, "Sorted input: %d passages\n", stats.InputPassages)
			if options.guard != nil {
				if e = options.guard(); e != nil {
					return e
				}
			}
		}
		return docs.add(key, b)
	}
	if options.QueueRoot != "" {
		e = walkCatalogQueues(options.QueueRoot, options.Collection, options.Metadata, visit)
	} else {
		e = walkCompactSourceFiltered(input, expanded, options.Collection, options.Collections, visit)
	}
	if e != nil {
		return stats, e
	}
	if stats.InputPassages == 0 {
		return stats, errors.New("no input records")
	}
	f, e := os.OpenFile(filepath.Join(temp, "index.bin"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return stats, e
	}
	defer f.Close()
	if _, e = f.Write(make([]byte, compactHeaderBytes)); e != nil {
		return stats, e
	}
	directory := compactDirectory{}
	posts := newDiskSorter(ctx, sortdir, "postings", options.SortMiB<<20)
	posts.guard = options.guard
	var pageRaw bytes.Buffer
	var pageFirst, pageCount uint32
	collections := map[string]string{}
	ranges := map[string]compactRange{}
	flushPageBlock := func() error {
		if pageCount == 0 {
			return nil
		}
		l, e := writeCompactBlock(f, pageRaw.Bytes(), pageCount)
		if e != nil {
			return e
		}
		directory.Pages = append(directory.Pages, block{pageFirst, l})
		pageRaw.Reset()
		pageCount = 0
		return nil
	}
	var currentKey string
	var current doc
	var terms map[string]byte
	bodyUsed := 0
	addWords := func(text string, mask byte) {
		for _, w := range words(text) {
			terms[w] |= mask
		}
	}
	finishPage := func() error {
		if currentKey == "" {
			return nil
		}
		if stats.Pages == math.MaxUint32 {
			return errors.New("too many indexed pages")
		}
		id := stats.Pages
		r := ranges[current.Collection]
		if r.Count == 0 {
			r.First = id
		}
		r.Count++
		ranges[current.Collection] = r
		current.Text = ""
		current.Headings = nil
		current.Anchor = ""
		current.Passage = 0
		current.Coverage = ""
		encoded := encodeDocVersion(current, 2)
		if len(encoded) > 1<<20 {
			return errors.New("page metadata too large")
		}
		if pageRaw.Len()+len(encoded) > compactBlockBytes && pageCount > 0 {
			if e := flushPageBlock(); e != nil {
				return e
			}
		}
		if pageCount == 0 {
			pageFirst = id
		}
		pageRaw.Write(encoded)
		pageCount++
		stats.Pages++
		var idBytes [4]byte
		binary.BigEndian.PutUint32(idBytes[:], id)
		for w, mask := range terms {
			if e := posts.add(w+"\x00"+string(idBytes[:]), []byte{mask}); e != nil {
				return e
			}
		}
		return nil
	}
	e = docs.finish(func(row sortRecord) error {
		var d doc
		if e := json.Unmarshal(row.value, &d); e != nil {
			return e
		}
		key := pageKey(d)
		if key != currentKey {
			if e := finishPage(); e != nil {
				return e
			}
			currentKey = key
			current = d
			terms = map[string]byte{}
			bodyUsed = 0
			label := d.CollectionLabel
			if label == "" {
				label = d.Collection
			}
			collections[d.Collection] = label
		}
		addWords(d.Title, fieldTitle)
		addWords(strings.Join(d.Authors, " "), fieldAuthor)
		addWords(strings.Join(d.Subjects, " "), fieldSubject)
		addWords(strings.Join(d.Headings, " "), fieldHeading)
		if options.BodyWords == 0 {
			addWords(d.Text, fieldBody)
		} else if bodyUsed < options.BodyWords {
			tokens := strings.Fields(d.Text)
			n := options.BodyWords - bodyUsed
			if n > len(tokens) {
				n = len(tokens)
			}
			addWords(strings.Join(tokens[:n], " "), fieldBody)
			bodyUsed += n
		}
		if len(terms) > 1000000 {
			return errors.New("page vocabulary exceeds construction bound")
		}
		return nil
	})
	if e != nil {
		return stats, e
	}
	if e = finishPage(); e != nil {
		return stats, e
	}
	terms = nil
	if e = flushPageBlock(); e != nil {
		return stats, e
	}
	postStart, e := f.Seek(0, io.SeekCurrent)
	if e != nil {
		return stats, e
	}
	stats.PageBytes = postStart - compactHeaderBytes
	// Dictionary entries are spooled sequentially while sorted postings are merged.
	dictPath := filepath.Join(sortdir, "dictionary.run")
	df, e := os.OpenFile(dictPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return stats, e
	}
	defer df.Close()
	dw := bufio.NewWriterSize(df, 256<<10)
	var word string
	var listStart int64
	var listCount uint32
	var raw bytes.Buffer
	var first, last, chunkCount uint32
	var pendingID uint32
	var pendingMask byte
	havePending := false
	flushChunk := func() error {
		if chunkCount == 0 {
			return nil
		}
		p := compactPack(raw.Bytes())
		var h [28]byte
		for i, v := range []uint32{first, last, uint32(len(p)), uint32(raw.Len()), crc32.Checksum(p, compactCRC), chunkCount} {
			binary.LittleEndian.PutUint32(h[i*4:], v)
		}
		binary.LittleEndian.PutUint32(h[24:], crc32.Checksum(h[:24], compactCRC))
		if _, e := f.Write(h[:]); e != nil {
			return e
		}
		if _, e := f.Write(p); e != nil {
			return e
		}
		raw.Reset()
		chunkCount = 0
		return nil
	}
	emitPosting := func(id uint32, mask byte) error {
		if raw.Len() >= compactBlockBytes {
			if e := flushChunk(); e != nil {
				return e
			}
		}
		if chunkCount == 0 {
			first = id
			last = id
			putvar(&raw, 0)
		} else {
			if id <= last {
				return errors.New("postings not strictly increasing")
			}
			putvar(&raw, uint64(id-last))
			last = id
		}
		raw.WriteByte(mask)
		chunkCount++
		listCount++
		return nil
	}
	finishWord := func() error {
		if word == "" {
			return nil
		}
		if havePending {
			if e := emitPosting(pendingID, pendingMask); e != nil {
				return e
			}
			havePending = false
		}
		t := compactTerm{Word: word, Count: listCount}
		if listCount <= 32 {
			var inline bytes.Buffer
			putvar(&inline, uint64(first))
			inline.Write(raw.Bytes()[1:])
			t.Inline = inline.Bytes()
			raw.Reset()
			chunkCount = 0
		} else {
			if e := flushChunk(); e != nil {
				return e
			}
			end, e := f.Seek(0, io.SeekCurrent)
			if e != nil {
				return e
			}
			t.Off = uint64(listStart)
			t.Bytes = uint64(end - listStart)
		}
		b, _ := json.Marshal(t)
		if e := writeSortRecord(dw, sortRecord{word, b}); e != nil {
			return e
		}
		if stats.Terms == math.MaxUint32 {
			return errors.New("too many dictionary terms")
		}
		stats.Terms++
		return nil
	}
	e = posts.finish(func(row sortRecord) error {
		if len(row.key) < 5 || row.key[len(row.key)-5] != 0 || len(row.value) != 1 {
			return errors.New("invalid posting run")
		}
		w := row.key[:len(row.key)-5]
		id := binary.BigEndian.Uint32([]byte(row.key[len(row.key)-4:]))
		mask := row.value[0]
		if w != word {
			if e := finishWord(); e != nil {
				return e
			}
			word = w
			listCount = 0
			var e error
			listStart, e = f.Seek(0, io.SeekCurrent)
			if e != nil {
				return e
			}
		}
		if havePending && pendingID == id {
			pendingMask |= mask
			return nil
		}
		if havePending {
			if e := emitPosting(pendingID, pendingMask); e != nil {
				return e
			}
		}
		pendingID = id
		pendingMask = mask
		havePending = true
		return nil
	})
	if e == nil {
		e = finishWord()
	}
	if e == nil {
		e = dw.Flush()
	}
	ce := df.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return stats, e
	}
	if stats.Terms == 0 {
		return stats, errors.New("source contains no searchable words")
	}
	dictStart, e := f.Seek(0, io.SeekCurrent)
	if e != nil {
		return stats, e
	}
	stats.PostingBytes = dictStart - postStart
	dr, e := os.Open(dictPath)
	if e != nil {
		return stats, e
	}
	r := bufio.NewReaderSize(dr, 64<<10)
	var dictRaw bytes.Buffer
	var previous, blockFirst string
	var firstID, dictCount uint32
	var previousOff uint64
	flushDict := func() error {
		if dictCount == 0 {
			return nil
		}
		l, e := writeCompactBlock(f, dictRaw.Bytes(), dictCount)
		if e != nil {
			return e
		}
		directory.Words = append(directory.Words, compactDictionaryBlock{blockFirst, firstID, l})
		dictRaw.Reset()
		dictCount = 0
		previous = ""
		previousOff = 0
		return nil
	}
	var wordID uint32
	for {
		row, e := readSortRecord(r)
		if e == io.EOF {
			break
		}
		if e != nil {
			dr.Close()
			return stats, e
		}
		var t compactTerm
		if e = json.Unmarshal(row.value, &t); e != nil {
			dr.Close()
			return stats, e
		}
		if dictRaw.Len() >= compactBlockBytes {
			if e = flushDict(); e != nil {
				dr.Close()
				return stats, e
			}
		}
		if dictCount == 0 {
			blockFirst = t.Word
			firstID = wordID
		}
		prefix := 0
		for prefix < len(previous) && prefix < len(t.Word) && previous[prefix] == t.Word[prefix] {
			prefix++
		}
		putvar(&dictRaw, uint64(prefix))
		putstr(&dictRaw, t.Word[prefix:])
		putvar(&dictRaw, uint64(t.Count))
		putvar(&dictRaw, uint64(len(t.Inline)))
		if len(t.Inline) > 0 {
			dictRaw.Write(t.Inline)
		} else {
			putvar(&dictRaw, t.Off-previousOff)
			putvar(&dictRaw, t.Bytes)
			previousOff = t.Off
		}
		dictCount++
		wordID++
		previous = t.Word
	}
	dr.Close()
	if e = flushDict(); e != nil {
		return stats, e
	}
	if e = os.Remove(dictPath); e != nil {
		return stats, e
	}
	if e = os.Remove(sortdir); e != nil {
		return stats, e
	}
	dirStart, e := f.Seek(0, io.SeekCurrent)
	if e != nil {
		return stats, e
	}
	stats.DictionaryBytes = dirStart - dictStart
	var dirRaw bytes.Buffer
	putvar(&dirRaw, uint64(len(directory.Words)))
	for _, d := range directory.Words {
		putstr(&dirRaw, d.First)
		putvar(&dirRaw, uint64(d.FirstID))
		if e = binary.Write(&dirRaw, binary.LittleEndian, d.Loc); e != nil {
			return stats, e
		}
	}
	putvar(&dirRaw, uint64(len(directory.Pages)))
	for _, b := range directory.Pages {
		if e = binary.Write(&dirRaw, binary.LittleEndian, b); e != nil {
			return stats, e
		}
	}
	if dirRaw.Len() > maxCompactDirectory {
		return stats, errors.New("sparse directory exceeds memory budget; split into collection bundles")
	}
	dl, e := writeCompactBlock(f, dirRaw.Bytes(), 0)
	if e != nil {
		return stats, e
	}
	stats.BodyWords = options.BodyWords
	var header [compactHeaderBytes]byte
	copy(header[:8], compactMagic)
	binary.LittleEndian.PutUint64(header[8:], dl.Off)
	binary.LittleEndian.PutUint32(header[16:], dl.N)
	binary.LittleEndian.PutUint32(header[20:], dl.Raw)
	binary.LittleEndian.PutUint32(header[24:], dl.CRC)
	binary.LittleEndian.PutUint32(header[28:], stats.Pages)
	binary.LittleEndian.PutUint32(header[32:], stats.Terms)
	binary.LittleEndian.PutUint32(header[36:], uint32(int32(options.BodyWords)))
	binary.LittleEndian.PutUint64(header[40:], uint64(postStart))
	binary.LittleEndian.PutUint64(header[48:], uint64(dictStart))
	binary.LittleEndian.PutUint64(header[56:], uint64(stats.InputPassages))
	binary.LittleEndian.PutUint32(header[64:], 1) // Compact encoding revision.
	binary.LittleEndian.PutUint32(header[124:], crc32.Checksum(header[:124], compactCRC))
	if _, e = f.WriteAt(header[:], 0); e != nil {
		return stats, e
	}
	if e = f.Sync(); e != nil {
		return stats, e
	}
	if e = f.Close(); e != nil {
		return stats, e
	}
	st, e := os.Stat(filepath.Join(temp, "index.bin"))
	if e != nil {
		return stats, e
	}
	stats.IndexBytes = st.Size()

	hashFile, e := os.Open(filepath.Join(temp, "index.bin"))
	if e != nil {
		return stats, e
	}
	hash := sha256.New()
	_, e = io.Copy(hash, hashFile)
	hashFile.Close()
	if e != nil {
		return stats, e
	}
	stats.BuildSeconds = time.Since(started).Seconds()
	m := compactManifest{IndexSHA256: hex.EncodeToString(hash.Sum(nil)), Version: 4, Ranges: ranges, Documents: int(stats.Pages), Shards: []string{"index.bin"}, Codec: "adaptive", BlockBytes: compactBlockBytes, Collections: collections, BodyWords: options.BodyWords, Stats: stats, Semantics: "AND across indexed fields on a page; no phrase or passage-proximity guarantee; only supplied source records are covered"}
	if options.QueueRoot != "" {
		m.SourceType = "catalog-queues"
	}
	ids, identityErr := compactSourceIdentities(options.QueueRoot, expanded, collections)
	if identityErr != nil {
		return stats, identityErr
	}
	m.Archives = ids
	if e = atomicJSON(filepath.Join(temp, "manifest.json"), m); e != nil {
		return stats, e
	}
	if options.guard != nil {
		if e = options.guard(); e != nil {
			return stats, e
		}
	}
	if _, e = os.Lstat(out); !os.IsNotExist(e) {
		return stats, errors.New("output appeared during build")
	}
	if e = os.Rename(temp, out); e != nil {
		return stats, e
	}
	return stats, nil
}

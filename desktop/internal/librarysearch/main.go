package librarysearch

import (
	"bufio"
	"bytes"
	"compress/flate"
	"container/list"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const magic = "ZSIDX001"
const magicV2 = "ZSIDX002"
const magicV3 = "ZSIDX003"

type doc struct {
	Collection      string   `json:"collection"`
	Title           string   `json:"title"`
	Path            string   `json:"path"`
	CollectionLabel string   `json:"collection_label,omitempty"`
	Authors         []string `json:"authors,omitempty"`
	Subjects        []string `json:"subjects,omitempty"`
	MetadataSource  string   `json:"metadata_source,omitempty"`
	Headings        []string `json:"headings,omitempty"`
	Text            string   `json:"text,omitempty"`
	Anchor          string   `json:"anchor,omitempty"`
	Passage         int      `json:"passage,omitempty"`
	Coverage        string   `json:"coverage,omitempty"`
}
type loc struct {
	Off                uint64
	N, Raw, CRC, Count uint32
}
type term struct {
	Word string
	Loc  loc
}
type block struct {
	First uint32
	Loc   loc
}
type manifest struct {
	Version     int
	Documents   int
	Shards      []string
	BlockBytes  int
	Codec       string
	Collections map[string]string `json:",omitempty"`
}

func words(s string) []string {
	a := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	sort.Strings(a)
	out := a[:0]
	for _, x := range a {
		if len(out) == 0 || out[len(out)-1] != x {
			out = append(out, x)
		}
	}
	return out
}
func putvar(w io.Writer, n uint64) { var b [10]byte; k := binary.PutUvarint(b[:], n); w.Write(b[:k]) }
func putstr(w io.Writer, s string) { putvar(w, uint64(len(s))); io.WriteString(w, s) }
func getstr(r *bytes.Reader) (string, error) {
	n, e := binary.ReadUvarint(r)
	if e != nil || n > uint64(r.Len()) {
		return "", errors.New("invalid string")
	}
	b := make([]byte, int(n))
	_, e = io.ReadFull(r, b)
	return string(b), e
}
func encodeDoc(d doc) []byte {
	var b bytes.Buffer
	putstr(&b, d.Collection)
	putstr(&b, d.Title)
	putstr(&b, d.Path)
	return b.Bytes()
}
func decodeDoc(r *bytes.Reader) (doc, error) {
	var d doc
	var e error
	if d.Collection, e = getstr(r); e != nil {
		return d, e
	}
	if d.Title, e = getstr(r); e != nil {
		return d, e
	}
	d.Path, e = getstr(r)
	return d, e
}

var compressorPool = sync.Pool{New: func() any { w, _ := flate.NewWriter(io.Discard, flate.BestSpeed); return w }}

func packed(raw []byte, codec string) []byte {
	if codec == "adaptive" {
		if len(raw) < 96 {
			return append([]byte{0}, raw...)
		}
		p := packed(raw, "flate")
		if len(p) >= len(raw) {
			return append([]byte{0}, raw...)
		}
		return append([]byte{1}, p...)
	}
	if codec == "none" {
		return raw
	}
	var b bytes.Buffer
	w := compressorPool.Get().(*flate.Writer)
	w.Reset(&b)
	w.Write(raw)
	w.Close()
	compressorPool.Put(w)
	return b.Bytes()
}
func unpack(p []byte, l loc, codec string) ([]byte, error) {
	if len(p) != int(l.N) || crc32.Checksum(p, crc32.MakeTable(crc32.Castagnoli)) != l.CRC {
		return nil, errors.New("block checksum mismatch")
	}
	if l.Raw > 16<<20 {
		return nil, errors.New("block too large")
	}
	if codec == "adaptive" {
		if len(p) == 0 {
			return nil, errors.New("missing block codec")
		}
		flag := p[0]
		p = p[1:]
		if flag == 0 {
			codec = "none"
		} else if flag == 1 {
			codec = "flate"
		} else {
			return nil, errors.New("unsupported block codec")
		}
	}
	if codec == "none" {
		if len(p) != int(l.Raw) {
			return nil, errors.New("invalid raw size")
		}
		return p, nil
	}
	r := flate.NewReader(bytes.NewReader(p))
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(r, int64(l.Raw)+1))
	if e != nil || len(b) != int(l.Raw) {
		return nil, errors.New("invalid decompressed size")
	}
	return b, nil
}
func writeBlock(f *os.File, raw []byte, codec string, count uint32) (loc, error) {
	p := packed(raw, codec)
	off, e := f.Seek(0, io.SeekCurrent)
	if e != nil {
		return loc{}, e
	}
	_, e = f.Write(p)
	return loc{uint64(off), uint32(len(p)), uint32(len(raw)), crc32.Checksum(p, crc32.MakeTable(crc32.Castagnoli)), count}, e
}
func buildShard(path string, docs []doc, blockSize int, codec string) error {
	return buildShardVersion(path, docs, blockSize, codec, 1)
}
func buildShardVersion(path string, docs []doc, blockSize int, codec string, version int) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write(make([]byte, 64)); e != nil {
		return e
	}
	posts := map[string][]uint32{}
	var blocks []block
	var raw bytes.Buffer
	first := uint32(0)
	count := uint32(0)
	flush := func() error {
		if count == 0 {
			return nil
		}
		l, e := writeBlock(f, raw.Bytes(), codec, count)
		if e != nil {
			return e
		}
		blocks = append(blocks, block{first, l})
		raw.Reset()
		count = 0
		return nil
	}
	for i, d := range docs {
		b := encodeDocVersion(d, version)
		if raw.Len()+len(b) > blockSize && count > 0 {
			if e = flush(); e != nil {
				return e
			}
		}
		if count == 0 {
			first = uint32(i)
		}
		raw.Write(b)
		count++
		for _, w := range documentTerms(d, version) {
			posts[w] = append(posts[w], uint32(i))
		}
	}
	if e = flush(); e != nil {
		return e
	}
	keys := make([]string, 0, len(posts))
	for w := range posts {
		keys = append(keys, w)
	}
	sort.Strings(keys)
	terms := make([]term, 0, len(keys))
	for _, w := range keys {
		var b bytes.Buffer
		var previous uint32
		for _, id := range posts[w] {
			putvar(&b, uint64(id-previous))
			previous = id
		}
		l, e := writeBlock(f, b.Bytes(), codec, uint32(len(posts[w])))
		if e != nil {
			return e
		}
		terms = append(terms, term{w, l})
	}
	dictOff, _ := f.Seek(0, io.SeekCurrent)
	binary.Write(f, binary.LittleEndian, uint32(len(terms)))
	prev := ""
	for _, t := range terms {
		prefix := 0
		for prefix < len(prev) && prefix < len(t.Word) && prev[prefix] == t.Word[prefix] {
			prefix++
		}
		putvar(f, uint64(prefix))
		putstr(f, t.Word[prefix:])
		if e = binary.Write(f, binary.LittleEndian, t.Loc); e != nil {
			return e
		}
		prev = t.Word
	}
	blockOff, _ := f.Seek(0, io.SeekCurrent)
	binary.Write(f, binary.LittleEndian, uint32(len(blocks)))
	for _, b := range blocks {
		if e = binary.Write(f, binary.LittleEndian, b); e != nil {
			return e
		}
	}
	var header [64]byte
	copy(header[:], magic)
	if version == 2 {
		copy(header[:], magicV2)
	}
	if version == 3 {
		copy(header[:], magicV3)
	}
	binary.LittleEndian.PutUint64(header[8:], uint64(dictOff))
	binary.LittleEndian.PutUint64(header[16:], uint64(blockOff))
	binary.LittleEndian.PutUint32(header[24:], uint32(len(docs)))
	if _, e = f.WriteAt(header[:], 0); e != nil {
		return e
	}
	return f.Sync()
}
func build(input, out string, maxDocs, shardDocs, blockBytes int, codec string) error {
	return buildEnriched(input, out, maxDocs, shardDocs, blockBytes, codec, nil)
}
func buildEnriched(input, out string, maxDocs, shardDocs, blockBytes int, codec string, meta *metadata) error {
	if codec != "none" && codec != "flate" && codec != "adaptive" {
		return errors.New("codec must be none, flate or adaptive")
	}
	if shardDocs < 1 || maxDocs < 1 || blockBytes < 1024 || blockBytes > 1<<20 {
		return errors.New("invalid build limits")
	}
	if e := os.Mkdir(out, 0700); e != nil {
		return e
	}
	f, e := os.Open(input)
	if e != nil {
		return e
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 65536), 4<<20)
	m := manifest{Version: 1, BlockBytes: blockBytes, Codec: codec}
	// Inspect the input schema without disturbing the streaming reader below.
	probe, err := os.Open(input)
	if err != nil {
		return err
	}
	var first doc
	err = json.NewDecoder(probe).Decode(&first)
	probe.Close()
	if err != nil {
		return err
	}
	deepInput := first.Passage > 0
	if meta != nil {
		m.Version = 2
		m.Collections = map[string]string{}
	}
	if deepInput {
		m.Version = 3
		m.Collections = map[string]string{}
	}
	docs := make([]doc, 0, shardDocs)
	flush := func() error {
		if len(docs) == 0 {
			return nil
		}
		name := fmt.Sprintf("shard-%04d.bin", len(m.Shards))
		if e := buildShardVersion(filepath.Join(out, name), docs, blockBytes, codec, m.Version); e != nil {
			return e
		}
		m.Shards = append(m.Shards, name)
		m.Documents += len(docs)
		docs = make([]doc, 0, shardDocs)
		return nil
	}
	for s.Scan() {
		var d doc
		if e = json.Unmarshal(s.Bytes(), &d); e != nil {
			return e
		}
		if deepInput != (d.Passage > 0) {
			return errors.New("mixed metadata and passage input is not supported")
		}
		d = enrich(d, meta)
		if m.Version >= 2 {
			m.Collections[d.Collection] = d.CollectionLabel
		}
		docs = append(docs, d)
		if len(docs) == shardDocs {
			if e = flush(); e != nil {
				return e
			}
		}
		if m.Documents+len(docs) >= maxDocs {
			break
		}
	}
	if e = s.Err(); e != nil {
		return e
	}
	if e = flush(); e != nil {
		return e
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(out, "manifest.json"), b, 0600)
}

type cached struct {
	key  string
	data []byte
}
type cache struct {
	max, used int
	ll        *list.List
	items     map[string]*list.Element
}

func newCache(max int) *cache {
	return &cache{max: max, ll: list.New(), items: map[string]*list.Element{}}
}
func (c *cache) get(k string) ([]byte, bool) {
	e, ok := c.items[k]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(e)
	return e.Value.(cached).data, true
}
func (c *cache) add(k string, b []byte) {
	if len(b) > c.max {
		return
	}
	for c.used+len(b) > c.max && c.ll.Len() > 0 {
		e := c.ll.Back()
		v := e.Value.(cached)
		delete(c.items, v.key)
		c.used -= len(v.data)
		c.ll.Remove(e)
	}
	c.items[k] = c.ll.PushFront(cached{k, b})
	c.used += len(b)
}

type shard struct {
	f       *os.File
	terms   []term
	blocks  []block
	count   uint32
	version int
}
type engine struct {
	shards           []shard
	codec            string
	cache            *cache
	readBytes, reads uint64
}

func openIndex(dir string, cacheMiB int) (*engine, error) {
	data, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		return nil, e
	}
	var m manifest
	if e = json.Unmarshal(data, &m); e != nil {
		return nil, e
	}
	if (m.Version != 1 && m.Version != 2 && m.Version != 3) || (m.Codec != "flate" && m.Codec != "none" && m.Codec != "adaptive") {
		return nil, errors.New("unsupported index")
	}
	en := &engine{codec: m.Codec, cache: newCache(cacheMiB << 20)}
	var dictionaryEstimate int
	for _, name := range m.Shards {
		if filepath.Base(name) != name {
			return nil, errors.New("invalid shard path")
		}
		f, e := os.Open(filepath.Join(dir, name))
		if e != nil {
			return nil, e
		}
		var h [64]byte
		if _, e = io.ReadFull(f, h[:]); e != nil || (string(h[:8]) != magic && string(h[:8]) != magicV2 && string(h[:8]) != magicV3) {
			return nil, errors.New("invalid header")
		}
		if (m.Version == 1 && string(h[:8]) != magic) || (m.Version == 2 && string(h[:8]) != magicV2) || (m.Version == 3 && string(h[:8]) != magicV3) {
			return nil, errors.New("manifest/shard version mismatch")
		}
		dictOff := binary.LittleEndian.Uint64(h[8:])
		blockOff := binary.LittleEndian.Uint64(h[16:])
		st, _ := f.Stat()
		if dictOff < 64 || blockOff < dictOff || blockOff > uint64(st.Size()) || blockOff-dictOff > 48<<20 {
			return nil, errors.New("invalid directory offsets")
		}
		b := make([]byte, int(blockOff-dictOff))
		if _, e = f.ReadAt(b, int64(dictOff)); e != nil {
			return nil, e
		}
		en.readBytes += uint64(len(b) + 64)
		en.reads += 2
		r := bytes.NewReader(b)
		var n uint32
		if e = binary.Read(r, binary.LittleEndian, &n); e != nil || n > uint32(len(b)/26+1) {
			return nil, errors.New("invalid dictionary count")
		}
		sh := shard{version: m.Version, f: f, count: binary.LittleEndian.Uint32(h[24:]), terms: make([]term, 0, n)}
		prev := ""
		for i := uint32(0); i < n; i++ {
			prefix, e := binary.ReadUvarint(r)
			if e != nil || prefix > uint64(len(prev)) {
				return nil, errors.New("invalid word prefix")
			}
			suffix, e := getstr(r)
			if e != nil {
				return nil, e
			}
			t := term{Word: prev[:int(prefix)] + suffix}
			if e = binary.Read(r, binary.LittleEndian, &t.Loc); e != nil {
				return nil, e
			}
			dictionaryEstimate += len(t.Word) + 64
			if dictionaryEstimate > 48<<20 {
				return nil, errors.New("dictionary exceeds 48 MiB experiment budget")
			}
			sh.terms = append(sh.terms, t)
			prev = t.Word
		}
		if _, e = f.Seek(int64(blockOff), 0); e != nil {
			return nil, e
		}
		if e = binary.Read(f, binary.LittleEndian, &n); e != nil || n > sh.count {
			return nil, errors.New("invalid block count")
		}
		sh.blocks = make([]block, n)
		for i := range sh.blocks {
			if e = binary.Read(f, binary.LittleEndian, &sh.blocks[i]); e != nil {
				return nil, e
			}
		}
		en.shards = append(en.shards, sh)
	}
	return en, nil
}
func (e *engine) close() {
	for _, s := range e.shards {
		s.f.Close()
	}
}
func (e *engine) read(si int, l loc) ([]byte, error) {
	key := fmt.Sprintf("%d/%d", si, l.Off)
	if b, ok := e.cache.get(key); ok {
		return b, nil
	}
	if l.N > 16<<20 {
		return nil, errors.New("oversized block")
	}
	p := make([]byte, int(l.N))
	n, err := e.shards[si].f.ReadAt(p, int64(l.Off))
	e.reads++
	e.readBytes += uint64(n)
	if err != nil {
		return nil, err
	}
	b, err := unpack(p, l, e.codec)
	if err == nil {
		e.cache.add(key, b)
	}
	return b, err
}
func decodeIDs(raw []byte, count uint32) ([]uint32, error) {
	if uint64(count) > uint64(len(raw)) {
		return nil, errors.New("invalid posting count")
	}
	r := bytes.NewReader(raw)
	a := make([]uint32, 0, count)
	var v uint64
	for i := uint32(0); i < count; i++ {
		gap, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, err
		}
		v += gap
		if v > 1<<32-1 {
			return nil, errors.New("posting overflow")
		}
		a = append(a, uint32(v))
	}
	if r.Len() != 0 {
		return nil, errors.New("posting trailing bytes")
	}
	return a, nil
}
func intersect(a, b []uint32) []uint32 {
	out := a[:0]
	for i, j := 0, 0; i < len(a) && j < len(b); {
		if a[i] == b[j] {
			out = append(out, a[i])
			i++
			j++
		} else if a[i] < b[j] {
			i++
		} else {
			j++
		}
	}
	return out
}
func (e *engine) query(q string) (int, []doc, error) { return e.queryFiltered(q, "title", "") }
func (e *engine) queryFiltered(q, field, collection string) (int, []doc, error) {
	if field != "title" && field != "author" && field != "subject" && field != "all" && field != "heading" && field != "text" && field != "deep" {
		return 0, nil, errors.New("invalid search field")
	}
	if (field == "deep" || field == "text" || field == "heading") && len(e.shards) > 0 && e.shards[0].version < 3 {
		return 0, nil, errors.New("deep search requires an extracted-passage index")
	}
	ws := words(q)
	if len(ws) == 0 {
		return 0, nil, nil
	}
	var found []doc
	total := 0
	for si, s := range e.shards {
		ts := make([]term, 0, len(ws))
		for _, w := range ws {
			if s.version >= 2 {
				w = field + ":" + w
			} else if field != "title" && field != "all" && field != "heading" && field != "text" && field != "deep" {
				ts = nil
				break
			}
			i := sort.Search(len(s.terms), func(i int) bool { return s.terms[i].Word >= w })
			if i == len(s.terms) || s.terms[i].Word != w {
				ts = nil
				break
			}
			ts = append(ts, s.terms[i])
		}
		if len(ts) == 0 {
			continue
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i].Loc.Count < ts[j].Loc.Count })
		var ids []uint32
		for _, t := range ts {
			raw, err := e.read(si, t.Loc)
			if err != nil {
				return 0, nil, err
			}
			a, err := decodeIDs(raw, t.Loc.Count)
			if err != nil {
				return 0, nil, err
			}
			if ids == nil {
				ids = a
			} else {
				ids = intersect(ids, a)
			}
			if len(ids) == 0 {
				break
			}
		}
		if collection == "" {
			total += len(ids)
		}
		for _, id := range ids {
			if collection == "" && len(found) >= 20 {
				break
			}
			bi := sort.Search(len(s.blocks), func(i int) bool { return s.blocks[i].First > id }) - 1
			if bi < 0 {
				return 0, nil, errors.New("missing document block")
			}
			b := s.blocks[bi]
			raw, err := e.read(si, b.Loc)
			if err != nil {
				return 0, nil, err
			}
			r := bytes.NewReader(raw)
			var d doc
			for j := b.First; j <= id; j++ {
				d, err = decodeDocVersion(r, s.version)
				if err != nil {
					return 0, nil, err
				}
			}
			if collection != "" {
				if d.Collection != collection {
					continue
				}
				total++
			}
			if len(found) < 20 {
				found = append(found, d)
			}
		}
	}
	return total, found, nil
}
func reference(input, q string, n int) (int, error) {
	f, e := os.Open(input)
	if e != nil {
		return 0, e
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 65536), 4<<20)
	qs := words(q)
	count := 0
	for i := 0; i < n && s.Scan(); i++ {
		var d doc
		if e = json.Unmarshal(s.Bytes(), &d); e != nil {
			return 0, e
		}
		ws := words(d.Title)
		match := len(qs) > 0
		for _, q := range qs {
			j := sort.SearchStrings(ws, q)
			if j == len(ws) || ws[j] != q {
				match = false
				break
			}
		}
		if match {
			count++
		}
	}
	return count, s.Err()
}
func dirBytes(dir string) int64 {
	var size int64
	filepath.Walk(dir, func(_ string, i os.FileInfo, e error) error {
		if e == nil && !i.IsDir() {
			size += i.Size()
		}
		return nil
	})
	return size
}
func Command() {
	debug.SetMemoryLimit(192 << 20)
	f := flag.NewFlagSet("indexbench", flag.ExitOnError)
	input := f.String("input", "", "JSONL source")
	out := f.String("dir", "", "binary index directory")
	mode := f.String("mode", "build", "build, bench, serve, metadata, extract-deep, expand, sample, compact, pack, verify or bind")
	limit := f.Int("documents", 200000, "sample records")
	shardSize := f.Int("shard-documents", 25000, "records per shard")
	blockBytes := f.Int("block-bytes", 32768, "record block target")
	codec := f.String("codec", "flate", "adaptive, flate or none")
	cacheMiB := f.Int("cache-mib", 128, "decompressed block cache budget")
	queryList := f.String("queries", "water,solar,generator,repair,cheese,the,water solar", "comma-separated queries")
	evict := f.Bool("evict-file-pages", false, "Linux: request eviction of only this index file cache before each empty-cache query")
	label := f.String("storage-label", "unspecified; OS cache uncontrolled", "description of benchmark storage")
	metaFile := f.String("metadata", "", "optional sourced metadata JSON; creates version 2 index")
	catalog := f.String("catalog", "", "library catalog for metadata export")
	sources := f.String("sources", "", "ZIM source folder for metadata export")
	dump := f.String("zimdump", "zimdump", "metadata extractor")
	metadataOut := f.String("metadata-output", "", "new metadata JSON file")
	listen := f.String("listen", "127.0.0.1:8781", "search UI address")
	reader := f.String("reader", "http://127.0.0.1:8766", "Kiwix base URL")
	field := f.String("field", "title", "title, author, subject, all, heading, text or deep")
	collection := f.String("collection", "", "optional exact collection ID")
	deepOutput := f.String("deep-output", "", "new extracted-passage JSONL")
	deepPages := f.Int("pages", 50, "maximum successful pages for deep extraction")
	passageWords := f.Int("passage-words", 200, "words per deep-search passage")
	maxPassages := f.Int("max-passages-per-page", 1000, "maximum passages per page")
	expandConfigFile := f.String("config", "", "expanded-build JSON configuration")
	rounds := f.Int("rounds", 0, "expanded build rounds; 0 continues until all selected queues are processed")
	expandedRoot := f.String("expanded-root", "", "committed expanded-build directory to compact")
	bodyWords := f.Int("body-words", 200, "compact body words per page; -1 none, 0 all supplied body text")
	sortMiB := f.Int("sort-mib", 32, "compact external-sort buffer in MiB")
	volumeUUID := f.String("volume-uuid", "", "optional required output volume UUID")
	reserveGiB := f.Uint64("reserve-gib", 50, "output-volume free-space reserve when UUID is supplied")
	queueRoot := f.String("queue-root", "", "selected catalog queues for metadata-only compact index")
	forceCustom := f.Bool("force-custom", false, "rebuild native-indexed collections too (optional experiment)")
	customIndex := f.String("custom-index", "", "hybrid supplementary v4 index; native search uses -catalog and -reader")
	partMiB := f.Int64("part-mib", 1024, "maximum packed index part size in MiB")
	samplePages := f.Int("sample-pages", 1024, "uniform sample pages per collection")
	sampleSeed := f.Int64("sample-seed", 1, "reproducible sampling seed")
	f.Parse(os.Args[1:])
	if *mode == "bind" {
		if *sources == "" || *out == "" {
			panic("bind requires -sources and -dir")
		}
		if err := verifyCompact(context.Background(), *out); err != nil {
			panic(err)
		}
		if err := bindCompactArchives(*out, *sources); err != nil {
			panic(err)
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"bound": true, "directory": *out})
		return
	}
	if *mode == "verify" {
		if err := verifyCompact(context.Background(), *out); err != nil {
			panic(err)
		}
		result := map[string]any{"verified": true, "directory": *out}
		if *sources != "" {
			b, err := os.ReadFile(filepath.Join(*out, "manifest.json"))
			if err != nil {
				panic(err)
			}
			var m compactManifest
			if err = json.Unmarshal(b, &m); err != nil {
				panic(err)
			}
			matched, missing, err := matchArchiveIdentities(m.Archives, *sources)
			if err != nil {
				panic(err)
			}
			if len(missing) > 0 {
				panic(fmt.Sprintf("source archives missing or changed: %v", missing))
			}
			result["matched_archives"] = matched
		}
		json.NewEncoder(os.Stdout).Encode(result)
		return
	}
	if *mode == "pack" {
		if *input == "" || *out == "" {
			panic("pack requires -input source-index-directory and -dir new-pack-directory")
		}
		if *partMiB < 1 || *partMiB > 16384 {
			panic("part-mib must be 1-16384")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		var guard func() error
		if *volumeUUID != "" {
			if *reserveGiB > 1<<20 {
				panic("reserve too large")
			}
			guard = func() error { return volumeGuard(*out, *volumeUUID, *reserveGiB<<30) }
		}
		m, err := packCompact(ctx, *input, *out, *partMiB<<20, guard)
		if err != nil {
			panic(err)
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"parts": len(m.Parts), "bytes": m.Stats.IndexBytes, "sha256": m.IndexSHA256})
		return
	}
	if *mode == "sample" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := runSample(ctx, *expandConfigFile, *out, *samplePages, *sampleSeed); err != nil {
			panic(err)
		}
		return
	}
	if *mode == "compact" {
		if *out == "" {
			panic("provide -dir")
		}
		debug.SetMemoryLimit(1024 << 20)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		opt := compactOptions{QueueRoot: *queueRoot, Metadata: *metaFile, Collection: *collection, BodyWords: *bodyWords, SortMiB: *sortMiB}
		if *expandedRoot != "" && !*forceCustom {
			selected, err := prepareHybridSelection(*expandedRoot)
			if err != nil {
				panic(err)
			}
			if *collection != "" && !selected[*collection] {
				panic("collection already has native full-text search; use -force-custom only for an intentional duplicate index")
			}
			opt.Collections = selected
			fmt.Fprintf(os.Stderr, "Reusing native search; compacting %d supplementary collections\n", len(selected))
		}
		if *volumeUUID != "" {
			if *reserveGiB > 1<<20 {
				panic("reserve too large")
			}
			opt.guard = func() error { return volumeGuard(*out, *volumeUUID, *reserveGiB<<30) }
		}
		stats, err := buildCompact(ctx, *input, *expandedRoot, *out, opt)
		if err != nil {
			panic(err)
		}
		json.NewEncoder(os.Stdout).Encode(stats)
		return
	}
	if *mode == "expand" {
		debug.SetMemoryLimit(1024 << 20)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := runExpanded(ctx, *expandConfigFile, *rounds, *forceCustom); err != nil {
			panic(err)
		}
		return
	}
	if *mode != "build" && *mode != "bench" && *mode != "serve" && *mode != "metadata" && *mode != "extract-deep" {
		panic("unknown mode")
	}
	if *mode == "extract-deep" {
		var meta *metadata
		var err error
		if *metaFile != "" {
			meta, err = loadMetadata(*metaFile)
			if err != nil {
				panic(err)
			}
		}
		if err = extractDeep(*input, *sources, *dump, *deepOutput, meta, *deepPages, *passageWords, *maxPassages); err != nil {
			panic(err)
		}
		return
	}
	if *mode == "metadata" {
		if err := exportMetadata(*catalog, *sources, *dump, *metadataOut); err != nil {
			panic(err)
		}
		return
	}
	if *out == "" {
		panic("provide -dir")
	}
	start := time.Now()
	if *mode == "build" {
		var meta *metadata
		if *metaFile != "" {
			var err error
			meta, err = loadMetadata(*metaFile)
			if err != nil {
				panic(err)
			}
		}
		if e := buildEnriched(*input, *out, *limit, *shardSize, *blockBytes, *codec, meta); e != nil {
			panic(e)
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"build_seconds": time.Since(start).Seconds(), "index_bytes": dirBytes(*out)})
		return
	}
	if *cacheMiB < 0 || *cacheMiB > 128 {
		panic("cache budget must be 0-128 MiB")
	}
	var headerManifest struct{ Version int }
	mb, me := os.ReadFile(filepath.Join(*out, "manifest.json"))
	if me != nil {
		panic(me)
	}
	if me = json.Unmarshal(mb, &headerManifest); me != nil {
		panic(me)
	}
	if headerManifest.Version == 4 {
		if *mode == "serve" {
			en, err := openCompact(*out, *cacheMiB)
			if err != nil {
				panic(err)
			}
			defer en.close()
			var query queryEngine = en
			if *customIndex != "" {
				h, err := newHybridQuery(en, *out, *sources, *catalog, *customIndex, *reader)
				if err != nil {
					panic(err)
				}
				defer h.close()
				query = h
			} else if *sources != "" {
				available, err := newAvailableQuery(en, *out, *sources)
				if err != nil {
					panic(err)
				}
				query = available
			}
			if err = serveIndex(query, *out, *listen, *reader); err != nil {
				panic(err)
			}
			return
		}
		if err := benchCompact(*out, *input, *expandedRoot, *queueRoot, *queryList, *field, *collection, *label, *cacheMiB, *evict); err != nil {
			panic(err)
		}
		return
	}
	en, e := openIndex(*out, *cacheMiB)
	if e != nil {
		panic(e)
	}
	defer en.close()
	if *mode == "serve" {
		if err := serveIndex(en, *out, *listen, *reader); err != nil {
			panic(err)
		}
		return
	}
	var m manifest
	b, _ := os.ReadFile(filepath.Join(*out, "manifest.json"))
	json.Unmarshal(b, &m)
	var referenceMeta *metadata
	if *metaFile != "" {
		referenceMeta, e = loadMetadata(*metaFile)
		if e != nil {
			panic(e)
		}
	}
	rows := []map[string]any{}
	for _, q := range strings.Split(*queryList, ",") {
		want := -1
		if *input != "" {
			want, e = referenceFields(*input, q, m.Documents, *field, *collection, referenceMeta)
			if e != nil {
				panic(e)
			}
		}
		en.cache = newCache(*cacheMiB << 20)
		for _, run := range []string{"empty_app_cache", "warm_app_cache"} {
			if *evict && run == "empty_app_cache" {
				for _, sh := range en.shards {
					if err := evictFilePages(sh.f); err != nil {
						panic(err)
					}
				}
			}
			beforeB, beforeR := en.readBytes, en.reads
			beforePhysical, physicalErr := physicalReadBytes()
			start := time.Now()
			count, docs, e := en.queryFiltered(q, *field, *collection)
			if e != nil {
				panic(e)
			}
			if want >= 0 && want != count {
				panic(fmt.Sprintf("query %q got %d want %d", q, count, want))
			}
			elapsed := float64(time.Since(start).Microseconds()) / 1000
			afterPhysical, afterErr := physicalReadBytes()
			var storageBytes any
			if physicalErr == nil && afterErr == nil {
				storageBytes = afterPhysical - beforePhysical
			}
			rows = append(rows, map[string]any{"storage_read_bytes": storageBytes, "query": q, "run": run, "matches": count, "returned": len(docs), "milliseconds": elapsed, "read_bytes": en.readBytes - beforeB, "reads": en.reads - beforeR, "cache_bytes": en.cache.used})
		}
	}
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	json.NewEncoder(os.Stdout).Encode(map[string]any{"documents": m.Documents, "shards": len(m.Shards), "codec": m.Codec, "block_bytes": m.BlockBytes, "index_bytes": dirBytes(*out), "heap_alloc_bytes": ms.HeapAlloc, "go_sys_bytes": ms.Sys, "results": rows, "field": *field, "collection_filter": *collection, "storage": *label, "file_page_eviction_requested": *evict})
}

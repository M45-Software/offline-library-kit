package librarysearch

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
)

type compactRange struct{ First, Count uint32 }
type compactEngine struct {
	cachePrefix                    string
	f                              *compactStore
	size                           uint64
	postStart, dictStart, dirStart uint64
	pages, terms                   uint32
	bodyWords                      int
	directory                      compactDirectory
	cache                          *cache
	reads, readBytes               uint64
	ranges                         map[string]compactRange
}

func openCompact(dir string, cacheMiB int) (en *compactEngine, err error) {
	if cacheMiB < 0 || cacheMiB > 128 {
		return nil, errors.New("cache must be 0-128 MiB")
	}
	data, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		return nil, e
	}
	var m compactManifest
	if e = json.Unmarshal(data, &m); e != nil {
		return nil, e
	}
	if m.Version != 4 || len(m.Shards) != 1 || m.Shards[0] != "index.bin" || m.Codec != "adaptive" {
		return nil, errors.New("unsupported compact manifest")
	}
	f, e := openCompactStore(dir, m)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	if f.size < compactHeaderBytes {
		return nil, errors.New("short compact file")
	}
	var h [compactHeaderBytes]byte
	if _, e = f.ReadAt(h[:], 0); e != nil {
		return nil, e
	}
	if string(h[:8]) != compactMagic || binary.LittleEndian.Uint32(h[64:]) != 1 || binary.LittleEndian.Uint32(h[124:]) != crc32.Checksum(h[:124], compactCRC) {
		return nil, errors.New("invalid compact header")
	}
	dl := loc{Off: binary.LittleEndian.Uint64(h[8:]), N: binary.LittleEndian.Uint32(h[16:]), Raw: binary.LittleEndian.Uint32(h[20:]), CRC: binary.LittleEndian.Uint32(h[24:])}
	prefix, pe := filepath.Abs(dir)
	if pe != nil {
		return nil, pe
	}
	en = &compactEngine{cachePrefix: prefix + "|", f: f, size: uint64(f.size), postStart: binary.LittleEndian.Uint64(h[40:]), dictStart: binary.LittleEndian.Uint64(h[48:]), dirStart: dl.Off, pages: binary.LittleEndian.Uint32(h[28:]), terms: binary.LittleEndian.Uint32(h[32:]), bodyWords: int(int32(binary.LittleEndian.Uint32(h[36:]))), cache: newCache(cacheMiB << 20), ranges: m.Ranges}
	if en.pages == 0 || en.terms == 0 || en.bodyWords < -1 || en.bodyWords > 1000000 || m.Documents != int(en.pages) || m.BodyWords != en.bodyWords || en.postStart < compactHeaderBytes || en.dictStart < en.postStart || en.dirStart <= en.dictStart || dl.N == 0 || dl.Raw > maxCompactDirectory || dl.Off > en.size || uint64(dl.N) != en.size-dl.Off {
		return nil, errors.New("invalid compact section bounds")
	}
	raw, e := en.read(dl)
	if e != nil {
		return nil, e
	}
	r := bytes.NewReader(raw)
	n, e := binary.ReadUvarint(r)
	if e != nil || n == 0 || n > uint64(r.Len()/26) {
		return nil, errors.New("invalid sparse word count")
	}
	var nextWord uint64
	for i := uint64(0); i < n; i++ {
		first, e := getstr(r)
		if e != nil {
			return nil, e
		}
		id, e := binary.ReadUvarint(r)
		if e != nil || id != nextWord {
			return nil, errors.New("invalid sparse word ID")
		}
		var l loc
		if e = binary.Read(r, binary.LittleEndian, &l); e != nil {
			return nil, e
		}
		if first == "" || l.Count == 0 || !en.validLoc(l, en.dictStart, en.dirStart) || i > 0 && first <= en.directory.Words[len(en.directory.Words)-1].First {
			return nil, errors.New("invalid sparse word block")
		}
		nextWord += uint64(l.Count)
		en.directory.Words = append(en.directory.Words, compactDictionaryBlock{first, uint32(id), l})
	}
	if nextWord != uint64(en.terms) {
		return nil, errors.New("word count mismatch")
	}
	n, e = binary.ReadUvarint(r)
	if e != nil || n == 0 || n > uint64(r.Len()/28) {
		return nil, errors.New("invalid page block count")
	}
	var nextPage uint64
	for i := uint64(0); i < n; i++ {
		var b block
		if e = binary.Read(r, binary.LittleEndian, &b); e != nil {
			return nil, e
		}
		if uint64(b.First) != nextPage || b.Loc.Count == 0 || !en.validLoc(b.Loc, compactHeaderBytes, en.postStart) {
			return nil, errors.New("invalid page block")
		}
		nextPage += uint64(b.Loc.Count)
		en.directory.Pages = append(en.directory.Pages, b)
	}
	if nextPage != uint64(en.pages) || r.Len() != 0 {
		return nil, errors.New("page count or directory size mismatch")
	}
	var ranges []compactRange
	for name, v := range en.ranges {
		if name == "" || v.Count == 0 || uint64(v.First)+uint64(v.Count) > uint64(en.pages) {
			return nil, errors.New("invalid collection range")
		}
		ranges = append(ranges, v)
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].First < ranges[j].First })
	var end uint64
	for _, v := range ranges {
		if uint64(v.First) != end {
			return nil, errors.New("collection ranges must partition pages")
		}
		end += uint64(v.Count)
	}
	if end != uint64(en.pages) {
		return nil, errors.New("missing collection ranges")
	}
	en.cache = newCache(cacheMiB << 20)
	return en, nil
}
func (en *compactEngine) validLoc(l loc, start, end uint64) bool {
	return l.Off >= start && l.Off < end && uint64(l.N) <= end-l.Off && l.N > 0 && l.N <= 16<<20 && l.Raw <= 16<<20
}
func (en *compactEngine) read(l loc) ([]byte, error) {
	key := en.cachePrefix + fmt.Sprint(l.Off)
	if b, ok := en.cache.get(key); ok {
		return b, nil
	}
	if !en.validLoc(l, compactHeaderBytes, en.size) {
		return nil, errors.New("invalid block location")
	}
	p := make([]byte, l.N)
	n, e := en.f.ReadAt(p, int64(l.Off))
	en.reads++
	en.readBytes += uint64(n)
	if e != nil {
		return nil, e
	}
	b, e := unpack(p, l, "adaptive")
	if e != nil {
		return nil, e
	}
	en.cache.add(key, b)
	return b, nil
}
func (en *compactEngine) close() { en.f.Close() }
func (en *compactEngine) find(word string) (compactTerm, bool, error) {
	i := sort.Search(len(en.directory.Words), func(i int) bool { return en.directory.Words[i].First > word }) - 1
	if i < 0 {
		return compactTerm{}, false, nil
	}
	b := en.directory.Words[i]
	raw, e := en.read(b.Loc)
	if e != nil {
		return compactTerm{}, false, e
	}
	r := bytes.NewReader(raw)
	previous := ""
	var previousOff uint64
	for j := uint32(0); j < b.Loc.Count; j++ {
		pre, e := binary.ReadUvarint(r)
		if e != nil || pre > uint64(len(previous)) {
			return compactTerm{}, false, errors.New("invalid dictionary prefix")
		}
		suffix, e := getstr(r)
		if e != nil {
			return compactTerm{}, false, e
		}
		w := previous[:pre] + suffix
		count, e := binary.ReadUvarint(r)
		if e != nil || count == 0 || count > uint64(en.pages) {
			return compactTerm{}, false, errors.New("invalid posting count")
		}
		inlineN, e := binary.ReadUvarint(r)
		if e != nil || inlineN > 192 || inlineN > uint64(r.Len()) {
			return compactTerm{}, false, errors.New("invalid inline posting size")
		}
		t := compactTerm{Word: w, Count: uint32(count)}
		if inlineN > 0 {
			if count > 32 {
				return compactTerm{}, false, errors.New("inline posting count too large")
			}
			at := len(raw) - r.Len()
			t.Inline = raw[at : at+int(inlineN)]
			r.Seek(int64(inlineN), 1)
		} else {
			delta, e := binary.ReadUvarint(r)
			if e != nil || delta > en.dictStart-previousOff {
				return compactTerm{}, false, errors.New("invalid posting offset")
			}
			off := previousOff + delta
			length, e := binary.ReadUvarint(r)
			if e != nil || off < en.postStart || off >= en.dictStart || length < 28 || length > en.dictStart-off {
				return compactTerm{}, false, errors.New("invalid posting reference")
			}
			t.Off = off
			t.Bytes = length
			previousOff = off
		}
		if j == 0 && w != b.First || j > 0 && w <= previous {
			return compactTerm{}, false, errors.New("invalid dictionary order")
		}
		if w == word {
			return t, true, nil
		}
		if w > word {
			return compactTerm{}, false, nil
		}
		previous = w
	}
	if r.Len() != 0 {
		return compactTerm{}, false, errors.New("dictionary trailing bytes")
	}
	return compactTerm{}, false, nil
}

type compactIterator struct {
	en                              *compactEngine
	term                            compactTerm
	mask                            byte
	offset, end                     uint64
	seen                            uint32
	raw                             *bytes.Reader
	remain                          uint32
	blockFirst, blockLast, previous uint32
	first                           bool
	id                              uint32
	valid                           bool
	inline                          bool
}

func (en *compactEngine) iterator(t compactTerm, mask byte) *compactIterator {
	it := &compactIterator{en: en, term: t, mask: mask, offset: t.Off, end: t.Off + t.Bytes}
	if len(t.Inline) > 0 {
		it.inline = true
		it.raw = bytes.NewReader(t.Inline)
		it.remain = t.Count
		it.first = true
	}
	return it
}
func (it *compactIterator) advance(target uint32) (bool, error) {
	if it.valid && it.id >= target {
		return true, nil
	}
	it.valid = false
	if it.inline {
		for it.remain > 0 {
			delta, e := binary.ReadUvarint(it.raw)
			if e != nil {
				return false, e
			}
			if it.first {
				if delta >= uint64(it.en.pages) {
					return false, errors.New("inline page ID out of bounds")
				}
				it.previous = uint32(delta)
				it.first = false
			} else {
				if delta == 0 || delta >= uint64(it.en.pages)-uint64(it.previous) {
					return false, errors.New("invalid inline delta")
				}
				it.previous += uint32(delta)
			}
			mask, e := it.raw.ReadByte()
			if e != nil || mask == 0 || mask&^byte(31) != 0 {
				return false, errors.New("invalid inline field bits")
			}
			it.remain--
			if it.previous >= target && mask&it.mask != 0 {
				it.id = it.previous
				it.valid = true
				return true, nil
			}
		}
		if it.raw.Len() != 0 {
			return false, errors.New("inline posting trailing bytes")
		}
		return false, nil
	}
	for {
		if it.raw != nil && target > it.blockLast {
			it.raw = nil
			it.remain = 0
			it.previous = it.blockLast
		}
		if it.remain == 0 {
			if it.raw != nil && (it.raw.Len() != 0 || it.previous != it.blockLast) {
				return false, errors.New("posting block count mismatch")
			}
			it.raw = nil
			if it.offset == it.end {
				if it.seen != it.term.Count {
					return false, errors.New("posting list count mismatch")
				}
				return false, nil
			}
			if it.offset > it.end || it.end-it.offset < 28 {
				return false, errors.New("short posting block header")
			}
			var h [28]byte
			n, e := it.en.f.ReadAt(h[:], int64(it.offset))
			it.en.reads++
			it.en.readBytes += uint64(n)
			if e != nil {
				return false, e
			}
			if binary.LittleEndian.Uint32(h[24:]) != crc32.Checksum(h[:24], compactCRC) {
				return false, errors.New("posting header checksum mismatch")
			}
			first, last := binary.LittleEndian.Uint32(h[:4]), binary.LittleEndian.Uint32(h[4:])
			l := loc{Off: it.offset + 28, N: binary.LittleEndian.Uint32(h[8:]), Raw: binary.LittleEndian.Uint32(h[12:]), CRC: binary.LittleEndian.Uint32(h[16:]), Count: binary.LittleEndian.Uint32(h[20:])}
			if first > last || last >= it.en.pages || l.Count == 0 || uint64(l.Count) > uint64(last)-uint64(first)+1 || l.Raw > compactBlockBytes+8 || !it.en.validLoc(l, it.en.postStart, it.end) || it.seen > it.term.Count || l.Count > it.term.Count-it.seen || it.seen > 0 && first <= it.previous {
				return false, errors.New("invalid posting block bounds")
			}
			it.offset = l.Off + uint64(l.N)
			it.seen += l.Count
			if last < target {
				it.previous = last
				continue
			}
			raw, e := it.en.read(l)
			if e != nil {
				return false, e
			}
			it.raw = bytes.NewReader(raw)
			it.remain = l.Count
			it.blockFirst = first
			it.blockLast = last
			it.previous = first
			it.first = true
		}
		delta, e := binary.ReadUvarint(it.raw)
		if e != nil {
			return false, e
		}
		if it.first {
			if delta != 0 {
				return false, errors.New("invalid initial posting delta")
			}
			it.first = false
		} else {
			if delta == 0 || delta > uint64(it.blockLast)-uint64(it.previous) {
				return false, errors.New("invalid posting delta")
			}
			it.previous += uint32(delta)
		}
		mask, e := it.raw.ReadByte()
		if e != nil || mask == 0 || mask&^byte(31) != 0 {
			return false, errors.New("invalid posting field bits")
		}
		it.remain--
		if it.previous >= target && mask&it.mask != 0 {
			it.id = it.previous
			it.valid = true
			return true, nil
		}
	}
}
func (en *compactEngine) page(id uint32) (doc, error) {
	i := sort.Search(len(en.directory.Pages), func(i int) bool { return en.directory.Pages[i].First > id }) - 1
	if i < 0 {
		return doc{}, errors.New("page missing")
	}
	b := en.directory.Pages[i]
	if id-b.First >= b.Loc.Count {
		return doc{}, errors.New("page out of block")
	}
	raw, e := en.read(b.Loc)
	if e != nil {
		return doc{}, e
	}
	r := bytes.NewReader(raw)
	var d doc
	for j := b.First; j <= id; j++ {
		d, e = decodeDocVersion(r, 2)
		if e != nil {
			return doc{}, e
		}
	}
	if e = validateCompactDoc(d); e != nil {
		return doc{}, e
	}
	return d, nil
}
func (en *compactEngine) queryFiltered(q, field, collection string) (int, []doc, error) {
	mask, e := maskForField(field)
	if e != nil {
		return 0, nil, e
	}
	ws := words(q)
	if len(ws) == 0 {
		return 0, nil, nil
	}
	if len(ws) > 20 {
		return 0, nil, errors.New("at most 20 query words")
	}
	lower, upper := uint32(0), en.pages
	if collection != "" {
		r, ok := en.ranges[collection]
		if !ok {
			return 0, nil, nil
		}
		lower, upper = r.First, r.First+r.Count
	}
	var terms []compactTerm
	for _, w := range ws {
		t, ok, e := en.find(w)
		if e != nil {
			return 0, nil, e
		}
		if !ok {
			return 0, nil, nil
		}
		terms = append(terms, t)
	}
	sort.Slice(terms, func(i, j int) bool { return terms[i].Count < terms[j].Count })
	iters := make([]*compactIterator, len(terms))
	for i, t := range terms {
		iters[i] = en.iterator(t, mask)
	}
	count := 0
	var found []doc
	target := lower
	for {
		ok, e := iters[0].advance(target)
		if e != nil {
			return 0, nil, e
		}
		if !ok || iters[0].id >= upper {
			break
		}
		candidate := iters[0].id
		matched := true
		for _, it := range iters[1:] {
			ok, e := it.advance(candidate)
			if e != nil {
				return 0, nil, e
			}
			if !ok || it.id >= upper {
				return count, found, nil
			}
			if it.id > candidate {
				target = it.id
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		count++
		if len(found) < 20 {
			d, e := en.page(candidate)
			if e != nil {
				return 0, nil, e
			}
			found = append(found, d)
		}
		if candidate == mathMaxPage {
			return count, found, nil
		}
		target = candidate + 1
	}
	return count, found, nil
}

const mathMaxPage = ^uint32(0)

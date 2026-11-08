package librarysearch

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
)

func encodeDocVersion(d doc, version int) []byte {
	b := bytes.NewBuffer(encodeDoc(d))
	if version >= 2 {
		putstr(b, d.CollectionLabel)
		for _, list := range [][]string{d.Authors, d.Subjects} {
			putvar(b, uint64(len(list)))
			for _, s := range list {
				putstr(b, s)
			}
		}
		putstr(b, d.MetadataSource)
	}
	if version >= 3 {
		putvar(b, uint64(len(d.Headings)))
		for _, h := range d.Headings {
			putstr(b, h)
		}
		putstr(b, d.Text)
		putstr(b, d.Anchor)
		putvar(b, uint64(d.Passage))
		putstr(b, d.Coverage)
	}
	return b.Bytes()
}
func decodeDocVersion(r *bytes.Reader, version int) (doc, error) {
	d, e := decodeDoc(r)
	if e != nil || version == 1 {
		return d, e
	}
	if d.CollectionLabel, e = getstr(r); e != nil {
		return d, e
	}
	lists := []*[]string{&d.Authors, &d.Subjects}
	for _, list := range lists {
		n, err := binary.ReadUvarint(r)
		if err != nil || n > 256 {
			return d, errors.New("invalid metadata list")
		}
		for i := uint64(0); i < n; i++ {
			s, err := getstr(r)
			if err != nil {
				return d, err
			}
			*list = append(*list, s)
		}
	}
	d.MetadataSource, e = getstr(r)
	if e != nil {
		return d, e
	}
	if version >= 3 {
		n, err := binary.ReadUvarint(r)
		if err != nil || n > 6 {
			return d, errors.New("invalid heading list")
		}
		for i := uint64(0); i < n; i++ {
			h, err := getstr(r)
			if err != nil {
				return d, err
			}
			d.Headings = append(d.Headings, h)
		}
		if d.Text, e = getstr(r); e != nil {
			return d, e
		}
		if d.Anchor, e = getstr(r); e != nil {
			return d, e
		}
		number, err := binary.ReadUvarint(r)
		if err != nil || number > 1000000 {
			return d, errors.New("invalid passage number")
		}
		d.Passage = int(number)
		d.Coverage, e = getstr(r)
	}
	return d, e
}
func documentTerms(d doc, version int) []string {
	if version == 1 {
		return words(d.Title)
	}
	fields := map[string]string{"title": d.Title, "author": strings.Join(d.Authors, " "), "subject": strings.Join(d.Subjects, " ")}
	var out []string
	all := map[string]bool{}
	for field, text := range fields {
		for _, w := range words(text) {
			out = append(out, field+":"+w)
			all[w] = true
		}
	}
	for w := range all {
		out = append(out, "all:"+w)
	}
	if version >= 3 {
		for _, w := range words(strings.Join(d.Headings, " ")) {
			out = append(out, "heading:"+w)
		}
		for _, w := range words(d.Text) {
			out = append(out, "text:"+w)
		}
		for _, w := range words(d.Title + " " + strings.Join(d.Authors, " ") + " " + strings.Join(d.Subjects, " ") + " " + strings.Join(d.Headings, " ") + " " + d.Text) {
			out = append(out, "deep:"+w)
		}
	}
	sort.Strings(out)
	return out
}

func referenceFields(input, q string, n int, field, collection string, meta *metadata) (int, error) {
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
		d = enrich(d, meta)
		if collection != "" && d.Collection != collection {
			continue
		}
		text := d.Title
		switch field {
		case "author":
			text = strings.Join(d.Authors, " ")
		case "subject":
			text = strings.Join(d.Subjects, " ")
		case "all":
			text += " " + strings.Join(d.Authors, " ") + " " + strings.Join(d.Subjects, " ")
		case "heading":
			text = strings.Join(d.Headings, " ")
		case "text":
			text = d.Text
		case "deep":
			text += " " + strings.Join(d.Authors, " ") + " " + strings.Join(d.Subjects, " ") + " " + strings.Join(d.Headings, " ") + " " + d.Text
		case "title":
		default:
			return 0, errors.New("invalid field")
		}
		ws := words(text)
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

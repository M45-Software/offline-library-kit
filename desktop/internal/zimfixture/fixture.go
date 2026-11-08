// Package zimfixture creates synthetic archives for tests. It contains no
// publisher content and is never used by the desktop runtime.
package zimfixture

import (
	"crypto/md5"
	"encoding/binary"
	"os"
	"sort"
)

func Write(name string) error {
	return WriteWithHTML(name, `<html><body><li class="js-step">Unicorn repair instructions</li></body></html>`)
}

// WriteWithHTML substitutes one page to exercise reader bounds with synthetic data.
func WriteWithHTML(name, html string) error {
	le := binary.LittleEndian
	type item struct {
		ns         byte
		path, data string
		mime       uint16
	}
	items := []item{
		{'C', "Device/Links", `<html><body><a href="../Guide/Wrong/123?x=1#step">guide</a></body></html>`, 0},
		{'C', "Device/Empty", `<html><body>Empty</body></html>`, 0},
		{'C', "Guide/Correct/123", html, 0},
		{'C', "Guide/Empty/456", `<html><body>No steps</body></html>`, 0},
		{'M', "Title", "Synthetic repair test", 1}, {'M', "Name", "synthetic-ifixit-test", 1},
		{'M', "Language", "eng", 1}, {'M', "Date", "2026-11-06", 1},
		{'M', "Creator", "Offline Library Kit tests", 1}, {'M', "Publisher", "Offline Library Kit tests", 1},
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ns != items[j].ns {
			return items[i].ns < items[j].ns
		}
		return items[i].path < items[j].path
	})
	header := make([]byte, 80)
	le.PutUint32(header, 72173914)
	le.PutUint16(header[4:], 6)
	le.PutUint16(header[6:], 1)
	copy(header[8:24], "synthetic-fixture")
	le.PutUint32(header[24:], uint32(len(items)))
	le.PutUint32(header[28:], 1)
	le.PutUint64(header[56:], 80)
	le.PutUint32(header[64:], 1)
	le.PutUint32(header[68:], ^uint32(0))
	data := append(header, []byte("text/html\x00text/plain\x00\x00")...)
	pointers := make([]uint64, len(items))
	for i, it := range items {
		pointers[i] = uint64(len(data))
		en := make([]byte, 16)
		le.PutUint16(en, it.mime)
		en[3] = it.ns
		le.PutUint32(en[12:], uint32(i))
		data = append(data, en...)
		data = append(data, it.path...)
		data = append(data, 0)
		data = append(data, it.path...)
		data = append(data, 0)
	}
	le.PutUint64(data[32:], uint64(len(data)))
	for _, p := range pointers {
		var b [8]byte
		le.PutUint64(b[:], p)
		data = append(data, b[:]...)
	}
	le.PutUint64(data[40:], uint64(len(data)))
	for i := range items {
		var b [4]byte
		le.PutUint32(b[:], uint32(i))
		data = append(data, b[:]...)
	}
	le.PutUint64(data[48:], uint64(len(data)))
	var cluster [8]byte
	le.PutUint64(cluster[:], uint64(len(data)+8))
	data = append(data, cluster[:]...)
	data = append(data, 1)
	offsets := make([]byte, (len(items)+1)*4)
	pos := len(offsets)
	for i, it := range items {
		le.PutUint32(offsets[i*4:], uint32(pos))
		pos += len(it.data)
	}
	le.PutUint32(offsets[len(items)*4:], uint32(pos))
	data = append(data, offsets...)
	for _, it := range items {
		data = append(data, it.data...)
	}
	le.PutUint64(data[72:], uint64(len(data)))
	sum := md5.Sum(data)
	data = append(data, sum[:]...)
	return os.WriteFile(name, data, 0600)
}

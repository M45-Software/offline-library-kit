package librarysearch

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type collectionMetadata struct {
	Label    string   `json:"label"`
	Subjects []string `json:"subjects,omitempty"`
	Source   string   `json:"source"`
}
type bookMetadata struct {
	Title   string `json:"title"`
	Author  string `json:"author"`
	Subject string `json:"subject,omitempty"`
	Source  string `json:"source"`
}
type metadata struct {
	Collections map[string]collectionMetadata      `json:"collections"`
	Books       map[string]map[string]bookMetadata `json:"books"`
}

func loadMetadata(path string) (*metadata, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	var m metadata
	e = json.NewDecoder(io.LimitReader(f, 64<<20)).Decode(&m)
	return &m, e
}
func enrich(d doc, m *metadata) doc {
	if m == nil || d.Passage > 0 {
		return d
	}
	c := m.Collections[d.Collection]
	d.CollectionLabel = c.Label
	if d.CollectionLabel == "" {
		d.CollectionLabel = d.Collection
	}
	d.Subjects = append([]string(nil), c.Subjects...)
	d.MetadataSource = c.Source
	// Join only recognized book entry paths. A numeric suffix alone can also
	// identify an author directory and must never be treated as a book match.
	dot := strings.LastIndex(d.Path, ".")
	if dot < 0 {
		return d
	}
	id := d.Path[dot+1:]
	if _, e := strconv.Atoi(id); e != nil {
		return d
	}
	book, ok := m.Books[d.Collection][id]
	if !ok {
		return d
	}
	title := strings.ReplaceAll(book.Title, "/", "-")
	prefix := d.Path[:dot]
	coverTitle := title
	if r := []rune(coverTitle); len(r) > 230 {
		coverTitle = string(r[:230])
	}
	if prefix != title && prefix != coverTitle+"_cover" {
		return d
	}
	if strings.TrimSpace(book.Author) != "" {
		d.Authors = []string{book.Author}
	}
	if book.Subject != "" {
		d.Subjects = append(d.Subjects, "LCC "+book.Subject)
	}
	d.MetadataSource = book.Source
	return d
}
func exportMetadata(catalog, sources, dump, out string) error {
	f, e := os.Open(catalog)
	if e != nil {
		return e
	}
	var library struct {
		Books []struct {
			Path        string `xml:"path,attr"`
			Title       string `xml:"title,attr"`
			Description string `xml:"description,attr"`
		} `xml:"book"`
	}
	e = xml.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&library)
	f.Close()
	if e != nil {
		return e
	}
	m := metadata{Collections: map[string]collectionMetadata{}, Books: map[string]map[string]bookMetadata{}}
	total := 0
	for _, b := range library.Books {
		file := filepath.Base(b.Path)
		alias := strings.TrimSuffix(file, ".zim")
		source := filepath.Join(sources, file)
		if st, err := os.Stat(source); err != nil || !st.Mode().IsRegular() {
			continue
		}
		c := collectionMetadata{Label: b.Title, Source: "library catalog"}
		if strings.HasPrefix(b.Title, "Gutenberg: ") {
			c.Subjects = []string{strings.TrimPrefix(b.Title, "Gutenberg: ")}
		}
		m.Collections[alias] = c
		if !strings.HasPrefix(alias, "gutenberg_") {
			continue
		}
		cmd := exec.Command(dump, "show", "--url=full_by_popularity.js", source)
		pipe, e := cmd.StdoutPipe()
		if e != nil {
			return e
		}
		cmd.Stderr = os.Stderr
		if e = cmd.Start(); e != nil {
			return e
		}
		data, e := io.ReadAll(io.LimitReader(pipe, 32<<20+1))
		if e != nil || len(data) > 32<<20 {
			cmd.Process.Kill()
			cmd.Wait()
			return errors.New("book metadata too large or unreadable")
		}
		if e = cmd.Wait(); e != nil {
			return e
		}
		data = bytes.TrimSpace(data)
		prefix := []byte("var json_data = ")
		if !bytes.HasPrefix(data, prefix) {
			return errors.New("unrecognized Gutenberg book-list format")
		}
		data = bytes.TrimSpace(bytes.TrimSuffix(bytes.TrimSpace(data[len(prefix):]), []byte(";")))
		var rows [][]json.RawMessage
		if e = json.Unmarshal(data, &rows); e != nil {
			return e
		}
		books := map[string]bookMetadata{}
		for _, r := range rows {
			if len(r) < 4 {
				return errors.New("incomplete book metadata row")
			}
			var title, author, subject string
			var id int
			if e = json.Unmarshal(r[0], &title); e != nil {
				return e
			}
			if e = json.Unmarshal(r[1], &author); e != nil {
				return e
			}
			if e = json.Unmarshal(r[3], &id); e != nil || id <= 0 {
				return errors.New("invalid book identifier")
			}
			if len(r) > 4 {
				json.Unmarshal(r[4], &subject)
			}
			books[strconv.Itoa(id)] = bookMetadata{Title: title, Author: author, Subject: subject, Source: alias + ":full_by_popularity.js#" + strconv.Itoa(id)}
		}
		m.Books[alias] = books
		total += len(books)
	}
	data, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	output, e := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer output.Close()
	if _, e = output.Write(data); e != nil {
		return e
	}
	if e = output.Sync(); e != nil {
		return e
	}
	fmt.Fprintf(os.Stderr, "Imported %d collection labels and %d book metadata records\n", len(m.Collections), total)
	return nil
}

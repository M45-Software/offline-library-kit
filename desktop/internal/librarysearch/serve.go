package librarysearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const searchPage = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Offline Library Search</title><style>body{font:17px system-ui;color:#173126;background:#f0f5f1;max-width:62rem;margin:2rem auto;padding:0 1rem}h1{margin-bottom:.4rem}form,article{background:white;padding:1.2rem;border:1px solid #cbdccd;border-radius:12px;margin:1rem 0}label{display:block;font-size:.9rem;margin-bottom:.7rem}input,select{font:inherit;box-sizing:border-box;padding:.6rem;width:100%}button{font:inherit;background:#216343;color:white;padding:.6rem 1.2rem;border:0;border-radius:6px}small,p{line-height:1.5}small{color:#52695b}.filters{display:grid;grid-template-columns:1fr 1fr;gap:1rem}a{color:#175c3c}</style><h1>Search your offline library</h1><p>Search titles, or use author and subject metadata where available.</p>{{if .Coverage}}<p><small>{{.Coverage}}</small></p>{{end}}<form><label>Search words<input name="q" value="{{.Query}}" required maxlength="200" placeholder="For example: cheese, solar power, Bob Brown"></label><div class="filters"><label>Search in<select name="field"><option value="title" {{if eq .Field "title"}}selected{{end}}>Titles</option>{{if .Deep}}<option value="deep" {{if eq .Field "deep"}}selected{{end}}>Deep search: headings and text</option>{{if .Heading}}<option value="heading" {{if eq .Field "heading"}}selected{{end}}>Chapter and section headings</option>{{end}}{{if .Body}}<option value="text" {{if eq .Field "text"}}selected{{end}}>Body text</option>{{end}}{{end}}<option value="all" {{if eq .Field "all"}}selected{{end}}>Titles, authors and subjects</option><option value="author" {{if eq .Field "author"}}selected{{end}}>Authors</option><option value="subject" {{if eq .Field "subject"}}selected{{end}}>Subjects</option></select></label><label>Collection<select name="collection"><option value="">All collections</option>{{range $id,$label:=.Collections}}<option value="{{$id}}" {{if eq $id $.Collection}}selected{{end}}>{{$label}}</option>{{end}}</select></label></div><button>Search</button></form>{{if .Query}}<p>{{if .Estimated}}About {{end}}{{.Count}} matches; showing up to 20.</p>{{if .Notice}}<p>{{.Notice}}</p>{{end}}{{range .Results}}<article><a href="{{.URL}}">{{.Doc.Title}}</a><br><small>{{.Doc.CollectionLabel}}</small>{{if .Doc.Headings}}<p><strong>{{range .Doc.Headings}}{{.}} · {{end}}</strong></p>{{end}}{{if .Doc.Text}}<p>{{.Doc.Text}}</p><small>{{if .Doc.Passage}}Passage {{.Doc.Passage}} · {{end}}{{.Doc.Coverage}}</small>{{end}}{{if .Doc.Authors}}<p>Author: {{range .Doc.Authors}}{{.}} {{end}}</p>{{end}}{{if .Doc.Subjects}}<p>Subject: {{range .Doc.Subjects}}{{.}} · {{end}}</p>{{end}}{{if .Doc.MetadataSource}}<details><summary>Metadata source</summary><small>{{.Doc.MetadataSource}}</small></details>{{end}}</article>{{end}}{{end}}<p><small>Missing author and subject metadata is left unknown. Subject labels can describe the collection rather than a specific work. Deep search covers only extracted pages and passages; coverage depends on the selected index.</small></p></html>`

type viewResult struct {
	Doc doc
	URL string
}
type searchView struct {
	Query, Field, Collection string
	Collections              map[string]string
	Count                    int
	Deep                     bool
	Heading                  bool
	Estimated                bool
	Notice                   string
	Body                     bool
	Coverage                 string
	Results                  []viewResult
}

type queryEngine interface {
	queryFiltered(string, string, string) (int, []doc, error)
}

func serveIndex(en queryEngine, dir, address, reader string) error {
	host, _, e := net.SplitHostPort(address)
	if e != nil || host != "127.0.0.1" {
		return errors.New("listen must use 127.0.0.1")
	}
	u, e := url.Parse(reader)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil {
		return errors.New("reader must use http://127.0.0.1")
	}
	b, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		return e
	}
	var m manifest
	if e = json.Unmarshal(b, &m); e != nil {
		return e
	}
	coverage := ""
	deepAvailable := m.Version >= 3
	bodyAvailable := deepAvailable
	if m.Version == 4 {
		var cm compactManifest
		if e = json.Unmarshal(b, &cm); e != nil {
			return e
		}
		coverage = "Page-level search across the supplied extracted content. Open a result to read the original page. Body text covers all supplied passages."
		if cm.BodyWords < 0 {
			bodyAvailable = false
			coverage = "Page-level search covers metadata and extracted headings. Body text indexing is disabled."
		}
		if cm.BodyWords > 0 {
			coverage = fmt.Sprintf("Page-level search covers metadata, extracted headings and the first %d body words per page or book. Open a result to read the original page.", cm.BodyWords)
		}
		if cm.SourceType == "catalog-queues" {
			deepAvailable = false
			coverage = "Catalog search covers titles and available author/subject metadata. Headings and body text have not been indexed."
		}
	}
	t := template.Must(template.New("search").Parse(searchPage))
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		field := r.URL.Query().Get("field")
		if field == "" {
			field = "title"
		}
		collection := r.URL.Query().Get("collection")
		collections := m.Collections
		if available, ok := en.(interface {
			collectionAvailability() (map[string]string, error)
		}); ok {
			var err error
			collections, err = available.collectionAvailability()
			if err != nil {
				http.Error(w, "Collection inventory unavailable", 503)
				return
			}
		}
		if len(q) > 200 || len(words(q)) > 20 {
			http.Error(w, "Query too long", 400)
			return
		}
		if collection != "" {
			if _, ok := collections[collection]; !ok {
				http.Error(w, "Unknown collection", 400)
				return
			}
		}
		deep, heading, body, cov := deepAvailable, deepAvailable, bodyAvailable, coverage
		if features, ok := en.(interface {
			searchFeatures(string) (bool, bool, bool, string)
		}); ok {
			deep, heading, body, cov = features.searchFeatures(collection)
		}
		v := searchView{Query: q, Field: field, Collection: collection, Collections: collections, Deep: deep, Heading: heading, Body: body, Coverage: cov}
		if q != "" {
			mu.Lock()
			var count int
			var docs []doc
			var err error
			if detailed, ok := en.(interface {
				queryDetailed(context.Context, string, string, string) (searchOutcome, error)
			}); ok {
				out, e := detailed.queryDetailed(r.Context(), q, field, collection)
				err = e
				count, docs = out.Count, out.Docs
				v.Estimated = out.Estimated
				v.Notice = out.Notice
			} else {
				count, docs, err = en.queryFiltered(q, field, collection)
			}
			mu.Unlock()
			if err != nil {
				status := 400
				if errors.Is(err, errSearchUnavailable) {
					status = 503
				}
				http.Error(w, "Search unavailable: "+err.Error(), status)
				return
			}
			v.Count = count
			for _, d := range docs {
				valid := validateCompactDoc(d) == nil
				for _, p := range strings.Split(d.Path, "/") {
					if p == ".." || p == "." {
						valid = false
					}
				}
				if !valid {
					continue
				}
				target := url.URL{Path: "/content/" + d.Collection + "/" + d.Path, Fragment: d.Anchor}
				v.Results = append(v.Results, viewResult{d, strings.TrimSuffix(reader, "/") + target.String()})
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		t.Execute(w, v)
	})
	server := http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	return server.ListenAndServe()
}

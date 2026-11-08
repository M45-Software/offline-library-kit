package librarysearch

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

type nativeBook struct {
	Alias, Language string
	FullText        bool
}
type nativeClient struct {
	base      *url.URL
	client    *http.Client
	mu        sync.Mutex
	refreshed time.Time
	books     map[string]nativeBook
}

func newNativeClient(reader string) (*nativeClient, error) {
	u, e := url.Parse(reader)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("reader must be a loopback http URL without credentials, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	c := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != u.Scheme || r.URL.Host != u.Host {
			return errors.New("reader redirected outside its local origin")
		}
		if len(via) > 3 {
			return errors.New("too many reader redirects")
		}
		return nil
	}}
	return &nativeClient{base: u, client: c}, nil
}
func (n *nativeClient) get(ctx context.Context, path string, values url.Values) ([]byte, error) {
	u := *n.base
	u.Path += path
	u.RawQuery = values.Encode()
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return nil, e
	}
	resp, e := n.client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("reader search returned HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 2<<20 {
		return nil, errors.New("reader search response exceeds 2 MiB")
	}
	return b, nil
}
func (n *nativeClient) contentLink(link string) (alias, path, anchor string, err error) {
	u, e := url.Parse(link)
	if e != nil {
		return "", "", "", e
	}
	if u.Host != "" && (u.Host != n.base.Host || u.Scheme != n.base.Scheme) {
		return "", "", "", errors.New("reader returned an external result link")
	}
	if u.Scheme != "" && u.Scheme != n.base.Scheme {
		return "", "", "", errors.New("invalid result scheme")
	}
	prefix := n.base.Path + "/content/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", "", "", errors.New("reader returned a non-content link")
	}
	parts := strings.SplitN(strings.TrimPrefix(u.Path, prefix), "/", 2)
	alias = parts[0]
	if alias == "" || alias == "." || alias == ".." {
		return "", "", "", errors.New("invalid reader content name")
	}
	if len(parts) == 2 {
		path = parts[1]
	}
	return alias, path, u.Fragment, nil
}
func (n *nativeClient) inventory(ctx context.Context) (map[string]nativeBook, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.refreshed.IsZero() && time.Since(n.refreshed) < 10*time.Second {
		return n.books, nil
	}
	b, e := n.get(ctx, "/catalog/v2/entries", url.Values{"count": {"-1"}})
	if e != nil {
		return nil, e
	}
	var feed struct {
		XMLName xml.Name `xml:"feed"`
		Entries []struct {
			ID       string `xml:"id"`
			Language string `xml:"language"`
			Tags     string `xml:"tags"`
			Links    []struct {
				Type string `xml:"type,attr"`
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if e = xml.Unmarshal(b, &feed); e != nil {
		return nil, e
	}
	books := map[string]nativeBook{}
	for _, entry := range feed.Entries {
		uuid := strings.TrimPrefix(entry.ID, "urn:uuid:")
		var alias string
		for _, l := range entry.Links {
			if l.Type != "text/html" {
				continue
			}
			name, path, _, err := n.contentLink(l.Href)
			if err == nil && strings.Trim(path, "/") == "" {
				alias = name
				break
			}
		}
		if uuid == "" || alias == "" {
			continue
		}
		full := false
		for _, tag := range strings.Split(entry.Tags, ";") {
			if tag == "_ftindex:yes" {
				full = true
			}
		}
		books[uuid] = nativeBook{alias, entry.Language, full}
	}
	n.books = books
	n.refreshed = time.Now()
	return books, nil
}
func plainSnippet(raw string) string {
	z := html.NewTokenizer(strings.NewReader(raw))
	var out strings.Builder
	hidden := 0
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			break
		}
		t := z.Token()
		switch kind {
		case html.StartTagToken:
			if t.Data == "script" || t.Data == "style" {
				hidden++
			}
		case html.EndTagToken:
			if (t.Data == "script" || t.Data == "style") && hidden > 0 {
				hidden--
			}
		case html.TextToken:
			if hidden == 0 {
				out.WriteString(t.Data)
				out.WriteByte(' ')
			}
		}
	}
	text := strings.Join(strings.Fields(out.String()), " ")
	r := []rune(text)
	if len(r) > 1600 {
		text = string(r[:1600]) + "…"
	}
	return text
}
func (n *nativeClient) search(ctx context.Context, q string, group []collectionRoute, books map[string]nativeBook) (int, []doc, error) {
	values := url.Values{"pattern": {q}, "format": {"xml"}, "pageLength": {"20"}, "start": {"0"}}
	byAlias := map[string]collectionRoute{}
	for _, r := range group {
		b, ok := books[r.UUID]
		if !ok || !b.FullText {
			return 0, nil, errors.New("native archive is not ready in reader")
		}
		values.Add("books.id", r.UUID)
		byAlias[b.Alias] = r
	}
	if len(group) == 0 {
		return 0, nil, nil
	}
	raw, e := n.get(ctx, "/search", values)
	if e != nil {
		return 0, nil, e
	}
	var rss struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Total string `xml:"totalResults"`
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				Description struct {
					Inner string `xml:",innerxml"`
				} `xml:"description"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Entity = xml.HTMLEntity
	if e = decoder.Decode(&rss); e != nil {
		return 0, nil, e
	}
	countText := strings.Map(func(r rune) rune {
		if r == ',' || r == ' ' || r == '\u00a0' {
			return -1
		}
		return r
	}, strings.TrimSpace(rss.Channel.Total))
	count, e := strconv.ParseUint(countText, 10, 63)
	if e != nil {
		return 0, nil, errors.New("reader returned an invalid result count")
	}
	docs := make([]doc, 0, len(rss.Channel.Items))
	seen := map[string]bool{}
	for _, item := range rss.Channel.Items {
		alias, path, anchor, e := n.contentLink(item.Link)
		if e != nil {
			return 0, nil, e
		}
		route, ok := byAlias[alias]
		if !ok {
			return 0, nil, errors.New("reader returned a result from an unselected collection")
		}
		d := doc{Collection: alias, CollectionLabel: route.Label, Title: item.Title, Path: path, Anchor: anchor, Text: plainSnippet(item.Description.Inner), Coverage: "Full-text search result"}
		if e = validateCompactDoc(d); e != nil {
			continue
		}
		key := alias + "\x00" + path
		if seen[key] {
			continue
		}
		seen[key] = true
		docs = append(docs, d)
		if len(docs) == 20 {
			break
		}
	}
	return int(count), docs, nil
}

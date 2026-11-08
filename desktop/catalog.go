package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const kiwixCatalog = "https://library.kiwix.org/catalog/v2/"

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type catalogLink struct {
	Rel    string `xml:"rel,attr"`
	Type   string `xml:"type,attr"`
	Href   string `xml:"href,attr"`
	Length int64  `xml:"length,attr"`
}
type catalogEntry struct {
	ID       string        `xml:"id"`
	Title    string        `xml:"title"`
	Summary  string        `xml:"summary"`
	Language string        `xml:"language"`
	Updated  string        `xml:"updated"`
	Links    []catalogLink `xml:"link"`
}
type catalogFeed struct {
	Entries []catalogEntry `xml:"entry"`
	Total   int            `xml:"totalResults"`
}
type catalogItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Language    string `json:"language"`
	Updated     string `json:"updated"`
	ApproxBytes int64  `json:"approxBytes"`
}

func catalogItemFor(entry catalogEntry) (catalogItem, bool) {
	id := strings.TrimPrefix(entry.ID, "urn:uuid:")
	if !uuidPattern.MatchString(id) {
		return catalogItem{}, false
	}
	for _, l := range entry.Links {
		if l.Type == "application/x-zim" && strings.Contains(l.Rel, "acquisition") {
			if _, err := canonicalMetalink(l.Href); err == nil {
				return catalogItem{ID: id, Title: entry.Title, Summary: entry.Summary, Language: entry.Language, Updated: entry.Updated, ApproxBytes: l.Length}, true
			}
		}
	}
	return catalogItem{}, false
}

// The publisher feed points at the Kiwix download service. Do not accept
// arbitrary URLs supplied by the page or metadata as publisher checksums.
func canonicalMetalink(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return "", errors.New("Invalid publisher metadata URL")
	}
	host := strings.ToLower(u.Hostname())
	if host != "download.kiwix.org" && !strings.HasSuffix(host, ".download.kiwix.org") {
		return "", errors.New("Unexpected publisher metadata host")
	}
	if !strings.HasPrefix(u.Path, "/zim/") || !strings.HasSuffix(u.Path, ".zim.meta4") || path.Clean(u.Path) != u.Path {
		return "", errors.New("Invalid publisher metadata path")
	}
	u.Host = "download.kiwix.org"
	return u.String(), nil
}
func getXML(ctx context.Context, client *http.Client, address string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	metadataClient := *client
	metadataClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		host := strings.ToLower(next.URL.Hostname())
		if len(via) >= 10 || next.URL.Scheme != "https" || next.URL.User != nil || next.URL.Port() != "" ||
			(host != "library.kiwix.org" && !strings.HasSuffix(host, ".library.kiwix.org") && host != "download.kiwix.org" && !strings.HasSuffix(host, ".download.kiwix.org")) {
			return errors.New("Unexpected publisher metadata redirect")
		}
		return nil
	}
	resp, err := metadataClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseDownloadError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if err != nil {
		return err
	}
	if len(data) > 4*1024*1024 {
		return errors.New("Publisher response is too large")
	}
	return xml.Unmarshal(data, out)
}
func searchCatalog(ctx context.Context, client *http.Client, base, query, language string, start int) ([]catalogItem, int, error) {
	u, err := url.Parse(base + "entries")
	if err != nil {
		return nil, 0, err
	}
	q := u.Query()
	q.Set("count", "20")
	q.Set("start", strconv.Itoa(start))
	if language != "" {
		q.Set("lang", language)
	}
	if query != "" {
		q.Set("q", query)
	}
	u.RawQuery = q.Encode()
	var f catalogFeed
	if err = getXML(ctx, client, u.String(), &f); err != nil {
		return nil, 0, err
	}
	items := make([]catalogItem, 0, len(f.Entries))
	for _, e := range f.Entries {
		if item, ok := catalogItemFor(e); ok {
			items = append(items, item)
		}
	}
	return items, f.Total, nil
}
func resolveCatalogItem(ctx context.Context, client *http.Client, base, id string) (downloadRequest, error) {
	if !uuidPattern.MatchString(id) {
		return downloadRequest{}, errors.New("Invalid collection ID")
	}
	var entry catalogEntry
	if err := getXML(ctx, client, base+"entry/"+id, &entry); err != nil {
		return downloadRequest{}, err
	}
	if strings.TrimPrefix(entry.ID, "urn:uuid:") != id {
		return downloadRequest{}, errors.New("Publisher returned a different collection")
	}
	var meta string
	for _, l := range entry.Links {
		if l.Type == "application/x-zim" && strings.Contains(l.Rel, "acquisition") {
			u, e := canonicalMetalink(l.Href)
			if e == nil {
				meta = u
				break
			}
		}
	}
	if meta == "" {
		return downloadRequest{}, errors.New("Publisher has no supported archive download")
	}
	var m struct {
		File struct {
			Name   string `xml:"name,attr"`
			Size   int64  `xml:"size"`
			Hashes []struct {
				Type  string `xml:"type,attr"`
				Value string `xml:",chardata"`
			} `xml:"hash"`
		} `xml:"file"`
	}
	if err := getXML(ctx, client, meta, &m); err != nil {
		return downloadRequest{}, err
	}
	name := path.Base(strings.TrimSuffix(meta, ".meta4"))
	if m.File.Name != name {
		return downloadRequest{}, errors.New("Publisher metadata filename mismatch")
	}
	var hash string
	for _, h := range m.File.Hashes {
		if strings.EqualFold(h.Type, "sha-256") {
			hash = strings.TrimSpace(h.Value)
			break
		}
	}
	req := downloadRequest{URL: strings.TrimSuffix(meta, ".meta4"), Name: name, Bytes: m.File.Size, SHA256: hash}
	if err := validRequest(req); err != nil {
		return downloadRequest{}, fmt.Errorf("Publisher metadata: %w", err)
	}
	return req, nil
}

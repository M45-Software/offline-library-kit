package librarysearch

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type collectionRoute struct{ Collection, Label, UUID, Language, Backend string }

func readSearchPolicy(path string) (map[string]collectionRoute, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	var library struct {
		Books []struct {
			Path     string `xml:"path,attr"`
			ID       string `xml:"id,attr"`
			Title    string `xml:"title,attr"`
			Language string `xml:"language,attr"`
			Tags     string `xml:"tags,attr"`
		} `xml:"book"`
	}
	if e = xml.NewDecoder(f).Decode(&library); e != nil {
		return nil, e
	}
	routes := map[string]collectionRoute{}
	for _, b := range library.Books {
		if b.Path == "" {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(b.Path), ".zim")
		if name == "" || name == "." || name == ".." {
			return nil, errors.New("invalid search policy collection")
		}
		backend := "custom"
		for _, tag := range strings.Split(b.Tags, ";") {
			if tag == "_ftindex:yes" {
				backend = "native"
			}
		}
		lang := b.Language
		if lang == "" {
			lang = "und:" + name
		}
		r := collectionRoute{name, b.Title, b.ID, lang, backend}
		if r.Label == "" {
			r.Label = name
		}
		if _, exists := routes[name]; exists {
			return nil, fmt.Errorf("duplicate policy collection: %s", name)
		}
		routes[name] = r
	}
	if len(routes) == 0 {
		return nil, errors.New("search policy catalog is empty")
	}
	return routes, nil
}
func policyForExpanded(root string) (map[string]collectionRoute, error) {
	return readSearchPolicy(filepath.Join(root, "reader-library.xml"))
}
func customSelection(routes map[string]collectionRoute) map[string]bool {
	out := map[string]bool{}
	for name, r := range routes {
		if r.Backend == "custom" {
			out[name] = true
		}
	}
	return out
}
func prepareHybridSelection(root string) (map[string]bool, error) {
	routes, e := policyForExpanded(root)
	if e != nil {
		return nil, e
	}
	selection := customSelection(routes)
	b, e := os.ReadFile(filepath.Join(root, "plan.json"))
	if e != nil {
		return nil, e
	}
	var p expandPlan
	if e = json.Unmarshal(b, &p); e != nil {
		return nil, e
	}
	var unfinished []string
	for _, q := range p.Queues {
		if !selection[q.Name] {
			continue
		}
		s, e := loadExpandState(filepath.Join(root, "collections", q.Name))
		if e != nil {
			return nil, e
		}
		if !s.Done {
			unfinished = append(unfinished, q.Name)
		}
	}
	if len(unfinished) > 0 {
		sort.Strings(unfinished)
		return nil, fmt.Errorf("custom extraction is unfinished for: %v", unfinished)
	}
	return selection, nil
}

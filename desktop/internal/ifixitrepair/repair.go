package repair

import (
	"bytes"
	"fmt"
	xhtml "golang.org/x/net/html"
	stdhtml "html"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var hrefPattern = regexp.MustCompile(`(?i)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)')`)

const repairMarker = `data-offline-library-repair="1"`

type resolver struct {
	paths  map[string]int
	unique map[string]string
}

func keyFor(p string) string {
	bits := strings.Split(p, "/")
	if len(bits) > 1 && bits[0] == "Guide" {
		return "Guide:" + bits[len(bits)-1]
	}
	if len(bits) > 1 && bits[0] == "User" {
		return "User:" + bits[1]
	}
	if strings.HasPrefix(p, "Device/") || strings.HasPrefix(p, "Info/") {
		return "Fold:" + strings.ToLower(strings.ReplaceAll(p, "\u00a0", " "))
	}
	return ""
}
func newResolver(paths map[string]int) *resolver {
	r := &resolver{paths: paths, unique: map[string]string{}}
	for p := range paths {
		key := keyFor(p)
		if key == "" {
			continue
		}
		if prior, ok := r.unique[key]; ok && prior != p {
			r.unique[key] = ""
		} else if !ok {
			r.unique[key] = p
		}
	}
	return r
}
func targetFor(source, raw string) (string, *url.URL) {
	u, e := url.Parse(stdhtml.UnescapeString(raw))
	if e != nil || u.Scheme != "" || u.Host != "" || u.Path == "" {
		return "", nil
	}
	var target string
	if strings.HasPrefix(u.Path, "/") {
		target = strings.TrimLeft(u.Path, "/")
	} else {
		target = path.Clean(path.Join(path.Dir(source), u.Path))
	}
	return target, u
}
func relativeURL(source, target string, u *url.URL) string {
	a := strings.Split(path.Dir(source), "/")
	if len(a) == 1 && a[0] == "." {
		a = nil
	}
	b := strings.Split(target, "/")
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	parts := []string{}
	for i := n; i < len(a); i++ {
		parts = append(parts, "..")
	}
	for _, p := range b[n:] {
		parts = append(parts, url.PathEscape(p))
	}
	result := strings.Join(parts, "/")
	if u.RawQuery != "" {
		result += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		result += "#" + url.PathEscape(u.Fragment)
	}
	return result
}
func (r *resolver) rewrite(source string, raw []byte) ([]byte, int) {
	changes := 0
	result := hrefPattern.ReplaceAllFunc(raw, func(m []byte) []byte {
		v := hrefPattern.FindSubmatch(m)
		href := v[1]
		if href == nil {
			href = v[2]
		}
		target, u := targetFor(source, string(href))
		if u == nil {
			return m
		}
		if _, ok := r.paths[target]; ok {
			return m
		}
		fixed := r.unique[keyFor(target)]
		if base, fragment, ok := strings.Cut(target, "#"); fixed == "" && ok && u.Fragment == "" {
			if _, exists := r.paths[base]; exists {
				fixed = base
				u.Fragment = fragment
			}
		}
		if fixed == "" {
			return m
		}
		changes++
		return []byte(`href="` + stdhtml.EscapeString(relativeURL(source, fixed, u)) + `"`)
	})
	return result, changes
}

type facts struct {
	steps, guideLinks, childLinks int
	wikiText, description, media  bool
}
type frame struct {
	tag, id     string
	description bool
}

func repairHTML(source string, data []byte, r *resolver) ([]byte, int, bool, error) {
	z := xhtml.NewTokenizer(bytes.NewReader(data))
	var out bytes.Buffer
	stack := []frame{}
	f := facts{}
	changes := 0
	bodyPos := -1
	voids := map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true}
	for {
		kind := z.Next()
		raw := append([]byte(nil), z.Raw()...)
		if kind == xhtml.ErrorToken {
			if z.Err() != io.EOF {
				return nil, 0, false, z.Err()
			}
			break
		}
		switch kind {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			t := z.Token()
			attrs := map[string]string{}
			for _, a := range t.Attr {
				attrs[a.Key] = a.Val
			}
			wiki := false
			for _, s := range stack {
				wiki = wiki || s.id == "wikiRenderedText"
			}
			if wiki && (t.Data == "img" || t.Data == "video" || t.Data == "iframe") {
				f.media = true
			}
			if t.Data == "li" {
				for _, c := range strings.Fields(attrs["class"]) {
					if c == "js-step" {
						f.steps++
					}
				}
			}
			if t.Data == "a" {
				target, _ := targetFor(source, attrs["href"])
				if strings.HasPrefix(target, "Guide/") {
					f.guideLinks++
				}
				if strings.HasPrefix(target, "Device/") {
					f.childLinks++
				}
			}
			fixed, n := r.rewrite(source, raw)
			out.Write(fixed)
			changes += n
			if t.Data == "body" && bodyPos < 0 {
				bodyPos = out.Len()
			}
			if kind != xhtml.SelfClosingTagToken && !voids[t.Data] {
				stack = append(stack, frame{t.Data, attrs["id"], attrs["itemprop"] == "description"})
			}
			continue
		case xhtml.EndTagToken:
			t := z.Token()
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == t.Data {
					stack = stack[:i]
					break
				}
			}
		case xhtml.TextToken:
			t := z.Token()
			if strings.TrimSpace(t.Data) != "" {
				suppressed := false
				for _, s := range stack {
					if s.tag == "script" || s.tag == "style" || s.tag == "svg" {
						suppressed = true
					}
				}
				if !suppressed {
					for _, s := range stack {
						f.wikiText = f.wikiText || s.id == "wikiRenderedText"
						f.description = f.description || s.description
					}
				}
			}
		}
		out.Write(raw)
	}
	reason := ""
	if strings.HasPrefix(source, "Device/") && f.guideLinks == 0 && f.childLinks == 0 && !f.wikiText && !f.description && !f.media {
		reason = "This archived device page has no description, repair-guide links, or child devices."
	}
	if strings.HasPrefix(source, "Guide/") && f.steps == 0 {
		reason = "This archived guide has no step-by-step sections."
	}
	notice := reason != "" && !bytes.Contains(data, []byte(repairMarker))
	result := out.Bytes()
	if notice {
		if bodyPos < 0 {
			return nil, 0, false, fmt.Errorf("cannot insert notice without a body: %s", source)
		}
		note := []byte(`<aside ` + repairMarker + ` role="note" style="background:#fff3cd;color:#332701;border:2px solid #9b7700;padding:1rem;margin:1rem"><strong>Offline coverage is incomplete.</strong> ` + reason + ` This local notice does not restore missing source content.</aside>`)
		result = append(append(append([]byte{}, result[:bodyPos]...), note...), result[bodyPos:]...)
	}
	if changes == 0 && !notice {
		return data, 0, false, nil
	}
	return result, changes, notice, nil
}

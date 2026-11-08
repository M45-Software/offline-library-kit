package librarysearch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/net/html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type htmlFrame struct {
	tag, id string
	skip    bool
}

func attr(t html.Token, key string) string {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func hidden(t html.Token) bool {
	switch t.Data {
	case "head", "script", "style", "noscript", "template", "svg", "canvas", "iframe", "nav", "aside", "footer", "header", "button", "select":
		return true
	}
	if attr(t, "id") == "pg-header" || attr(t, "id") == "pg-footer" {
		return true
	}
	for _, a := range t.Attr {
		if a.Key == "hidden" || a.Key == "aria-hidden" && a.Val == "true" {
			return true
		}
	}
	style := strings.ReplaceAll(strings.ToLower(attr(t, "style")), " ", "")
	if strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") {
		return true
	}
	for _, c := range strings.Fields(attr(t, "class")) {
		switch c {
		case "toc", "navbox", "sidebar", "mw-editsection", "noprint":
			return true
		}
	}
	return false
}
func voidTag(t string) bool {
	return strings.Contains(" area base br col embed hr img input link meta param source track wbr ", " "+t+" ")
}
func headingLevel(s string) int {
	if len(s) == 2 && s[0] == 'h' && s[1] >= '1' && s[1] <= '6' {
		return int(s[1] - '0')
	}
	return 0
}
func textBlock(s string) bool {
	switch s {
	case "p", "li", "div", "section", "article", "blockquote", "pre", "tr", "td", "br", "hr", "dl", "dt", "dd":
		return true
	}
	return false
}

func rootSelector(t html.Token) (string, int) {
	for _, c := range strings.Fields(attr(t, "class")) {
		if c == "mw-parser-output" {
			return "class:mw-parser-output", 1
		}
	}
	switch attr(t, "id") {
	case "mainbar":
		return "id:mainbar", 2
	case "mainBody":
		return "id:mainBody", 3
	}
	if t.Data == "main" {
		return "tag:main", 4
	}
	if t.Data == "article" {
		return "tag:article", 5
	}
	return "", 100
}
func preferredRoot(raw []byte) string {
	z := html.NewTokenizer(bytes.NewReader(raw))
	var stack []htmlFrame
	best := ""
	priority := 100
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			return best
		}
		t := z.Token()
		parent := len(stack) > 0 && stack[len(stack)-1].skip
		switch kind {
		case html.StartTagToken, html.SelfClosingTagToken:
			skip := parent || hidden(t)
			if !skip {
				selector, p := rootSelector(t)
				if p < priority {
					best = selector
					priority = p
				}
			}
			if kind == html.StartTagToken && !voidTag(t.Data) {
				stack = append(stack, htmlFrame{tag: t.Data, skip: skip})
			}
		case html.EndTagToken:
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == t.Data {
					stack = stack[:i]
					break
				}
			}
		}
	}
}

// Extraction is bounded per page and produces small passages rather than one
// enormous posting document for a complete book. No HTML is stored in snippets.
func passages(base doc, raw []byte, passageWords, maxPassages int) ([]doc, error) {
	if passageWords < 10 || maxPassages < 1 {
		return nil, errors.New("invalid passage limits")
	}
	z := html.NewTokenizer(bytes.NewReader(raw))
	z.SetMaxBuf(8 << 20)
	selector := preferredRoot(raw)
	inRoot := selector == ""
	rootSeen := false
	rootDepth := 0
	pendingAnchor := ""
	var stack []htmlFrame
	var text, heading strings.Builder
	var headings [6]string
	anchor, headingAnchor := "", ""
	activeHeading := 0
	var out []doc
	truncated := false
	emit := func() {
		ws := strings.Fields(text.String())
		text.Reset()
		var hs []string
		for _, s := range headings {
			if s != "" {
				hs = append(hs, s)
			}
		}
		if len(ws) == 0 && len(hs) == 0 {
			return
		}
		for len(ws) > 0 || len(ws) == 0 && len(hs) > 0 {
			if len(out) >= maxPassages {
				truncated = true
				return
			}
			n := len(ws)
			if n > passageWords {
				n = passageWords
			}
			d := base
			d.Headings = append([]string(nil), hs...)
			d.Text = strings.Join(ws[:n], " ")
			d.Anchor = anchor
			d.Passage = len(out) + 1
			d.Coverage = "HTML text extracted"
			out = append(out, d)
			ws = ws[n:]
			if len(ws) == 0 {
				break
			}
		}
	}
	closeHeading := func() {
		if activeHeading == 0 {
			return
		}
		label := strings.Join(strings.Fields(heading.String()), " ")
		heading.Reset()
		headings[activeHeading-1] = label
		for i := activeHeading; i < 6; i++ {
			headings[i] = ""
		}
		anchor = headingAnchor
		activeHeading = 0
	}
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if z.Err() != io.EOF {
				return nil, z.Err()
			}
			closeHeading()
			emit()
			break
		}
		t := z.Token()
		parentSkip := len(stack) > 0 && stack[len(stack)-1].skip
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			skip := parentSkip || hidden(t)
			selected, _ := rootSelector(t)
			if selector != "" && !rootSeen && !skip && selected == selector {
				inRoot = true
				rootSeen = true
				rootDepth = len(stack) + 1
			}
			if !skip && inRoot {
				if level := headingLevel(t.Data); level > 0 {
					closeHeading()
					emit()
					activeHeading = level
					headingAnchor = attr(t, "id")
					if headingAnchor == "" {
						headingAnchor = pendingAnchor
					}
					pendingAnchor = ""
					if headingAnchor == "" {
						for i := len(stack) - 1; i >= 0; i-- {
							if stack[i].id != "" && (stack[i].tag == "div" || stack[i].tag == "section") {
								headingAnchor = stack[i].id
								break
							}
						}
					}
				} else if t.Data == "a" && activeHeading == 0 {
					pendingAnchor = attr(t, "id")
					if pendingAnchor == "" {
						pendingAnchor = attr(t, "name")
					}
				} else if t.Data == "br" && activeHeading > 0 {
					heading.WriteByte(' ')
				} else if activeHeading > 0 && headingAnchor == "" {
					headingAnchor = attr(t, "id")
					if headingAnchor == "" && t.Data == "a" {
						headingAnchor = attr(t, "name")
					}
				} else if textBlock(t.Data) {
					text.WriteByte(' ')
				}
			}
			if tt == html.StartTagToken && !voidTag(t.Data) {
				stack = append(stack, htmlFrame{t.Data, attr(t, "id"), skip})
			}
		case html.EndTagToken:
			if !parentSkip && inRoot {
				if headingLevel(t.Data) == activeHeading && activeHeading > 0 {
					closeHeading()
				} else if textBlock(t.Data) {
					text.WriteByte(' ')
				}
			}
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == t.Data {
					if rootDepth > 0 && i+1 == rootDepth {
						inRoot = false
						rootDepth = 0
					}
					stack = stack[:i]
					break
				}
			}
		case html.TextToken:
			if !parentSkip && inRoot {
				if activeHeading > 0 {
					heading.WriteString(t.Data)
				} else {
					text.WriteString(t.Data)
					if strings.TrimSpace(t.Data) != "" {
						pendingAnchor = ""
					}
				}
			}
		}
	}
	if truncated {
		for i := range out {
			out[i].Coverage = "Partial page: passage limit reached"
		}
	}
	return out, nil
}

type deepReport struct {
	Pages          int                 `json:"pages"`
	Attempts       int                 `json:"attempts"`
	Passages       int                 `json:"passages"`
	Skipped        []map[string]string `json:"skipped"`
	TruncatedPages int                 `json:"truncated_pages"`
	PassageWords   int                 `json:"passage_words"`
	MaxHTMLBytes   int                 `json:"max_html_bytes"`
}

func extractDeep(input, sources, dump, output string, meta *metadata, maxPages, passageWords, maxPassages int) error {
	if maxPages < 1 || passageWords < 10 || maxPassages < 1 {
		return errors.New("invalid extraction limits")
	}
	if _, e := os.Lstat(output); !os.IsNotExist(e) {
		return errors.New("output exists or cannot be inspected")
	}
	if _, e := os.Lstat(output + ".report.json"); !os.IsNotExist(e) {
		return errors.New("report exists or cannot be inspected")
	}
	src, e := os.Open(input)
	if e != nil {
		return e
	}
	defer src.Close()
	f, e := os.OpenFile(output+".partial", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	encoder := json.NewEncoder(w)
	scan := bufio.NewScanner(src)
	scan.Buffer(make([]byte, 65536), 4<<20)
	report := deepReport{Skipped: []map[string]string{}, PassageWords: passageWords, MaxHTMLBytes: 2 << 20}
	for scan.Scan() {
		var d doc
		if e = json.Unmarshal(scan.Bytes(), &d); e != nil {
			return e
		}
		if d.Collection == "" || filepath.Base(d.Collection) != d.Collection || strings.ContainsAny(d.Collection, `/\`) {
			return errors.New("invalid archive name")
		}
		d = enrich(d, meta)
		// For Gutenberg, only recognized reading pages, not covers or author listings.
		if strings.HasPrefix(d.Collection, "gutenberg_") && (!strings.Contains(d.MetadataSource, ":full_by_popularity.js#") || strings.Contains(d.Path, "_cover.")) {
			continue
		}
		bad := strings.HasPrefix(d.Path, "/")
		for _, part := range strings.Split(d.Path, "/") {
			if part == ".." || part == "." {
				bad = true
			}
		}
		if bad {
			return errors.New("invalid archive entry path")
		}
		if report.Attempts >= maxPages*4 {
			break
		}
		report.Attempts++
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, dump, "show", "--url="+d.Path, filepath.Join(sources, d.Collection+".zim"))
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			return err
		}
		cmd.Stderr = io.Discard
		if err = cmd.Start(); err != nil {
			cancel()
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(pipe, int64(report.MaxHTMLBytes)+1))
		if err != nil || len(raw) > report.MaxHTMLBytes {
			cmd.Process.Kill()
			cmd.Wait()
			cancel()
			report.Skipped = append(report.Skipped, map[string]string{"path": d.Path, "reason": "HTML exceeded extraction byte limit or could not be read"})
			continue
		}
		err = cmd.Wait()
		cancel()
		if err != nil {
			report.Skipped = append(report.Skipped, map[string]string{"path": d.Path, "reason": "archive entry unavailable or timed out"})
			continue
		}
		chunks, err := passages(d, raw, passageWords, maxPassages)
		if err != nil {
			return err
		}
		if len(chunks) == 0 {
			report.Skipped = append(report.Skipped, map[string]string{"path": d.Path, "reason": "no visible text extracted"})
			continue
		}
		for _, chunk := range chunks {
			if e = encoder.Encode(chunk); e != nil {
				return e
			}
		}
		report.Pages++
		report.Passages += len(chunks)
		if strings.HasPrefix(chunks[0].Coverage, "Partial") {
			report.TruncatedPages++
		}
		fmt.Fprintf(os.Stderr, "Deep extraction %s: %d passages\n", d.Title, len(chunks))
		if report.Pages >= maxPages {
			break
		}
	}
	if e = scan.Err(); e != nil {
		return e
	}
	if report.Pages == 0 {
		return errors.New("no pages extracted; partial retained")
	}
	if e = w.Flush(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if e = os.WriteFile(output+".report.json", b, 0600); e != nil {
		return e
	}
	if e = os.Link(output+".partial", output); e != nil {
		return e
	}
	return os.Remove(output + ".partial")
}

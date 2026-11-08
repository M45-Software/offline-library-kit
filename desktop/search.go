package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	zim "offline-library-kit/desktop/internal/ifixitrepair"
	search "offline-library-kit/desktop/internal/librarysearch"
)

const searchPackAllowance uint64 = 700_000_000

func (a *app) searchRoot() string { return filepath.Join(a.root, "content", "search") }

func searchTreeReady(source, destination string) bool {
	for _, name := range []string{"catalog", "custom"} {
		want, e := search.ReadManifest(filepath.Join(source, name))
		if e != nil {
			return false
		}
		got, e := search.ReadManifest(filepath.Join(destination, name))
		if e != nil || got.IndexSHA256 != want.IndexSHA256 || len(got.Parts) != len(want.Parts) {
			return false
		}
		if len(want.Parts) == 0 {
			st, e := os.Lstat(filepath.Join(destination, name, "index.bin"))
			if e != nil || !st.Mode().IsRegular() || st.Size() != want.Stats.IndexBytes {
				return false
			}
		}
		for i, p := range want.Parts {
			if got.Parts[i] != p {
				return false
			}
			st, e := os.Lstat(filepath.Join(destination, name, p.Name))
			if e != nil || !st.Mode().IsRegular() || st.Size() != p.Bytes {
				return false
			}
		}
	}
	return true
}

func (a *app) copySearchTree(ctx context.Context, source, destination string, reserve int) error {
	return a.copySearchTreeChecked(ctx, source, destination, reserve, nil)
}
func (a *app) copySearchTreeChecked(ctx context.Context, source, destination string, reserve int, guard func() error) error {
	if guard != nil {
		if err := guard(); err != nil {
			return err
		}
	}
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(source, func(name string, entry fs.DirEntry, err error) error {
		if guard != nil {
			if guardErr := guard(); guardErr != nil {
				return guardErr
			}
		}
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = a.checkLibraryRoot(); err != nil {
			return err
		}
		if name != source && (strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), ".part")) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			if err = os.MkdirAll(target, 0755); err != nil {
				return err
			}
			st, e := os.Lstat(target)
			if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return errors.New("Search destination is not a normal directory")
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("Search files must be regular files")
		}
		dir := filepath.Dir(target)
		identity, err := directoryIdentity(dir)
		if err != nil {
			return err
		}
		check := func() error {
			if guard != nil {
				if err := guard(); err != nil {
					return err
				}
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			if e := a.checkLibraryRoot(); e != nil {
				return e
			}
			current, e := directoryIdentity(dir)
			if e != nil || !os.SameFile(identity, current) {
				return errors.New("Search destination changed during copy")
			}
			return nil
		}
		return a.copyOneStaged(ctx, name, dir, check, reserve)
	})
}
func (a *app) ensureSearchAssets(ctx context.Context) error {
	if a.searchDir == "" {
		return nil
	}
	if searchTreeReady(a.searchDir, a.searchRoot()) {
		return nil
	}
	a.mu.Lock()
	reserve := a.effectiveReserveGiB()
	a.mu.Unlock()
	return a.copySearchTree(ctx, a.searchDir, a.searchRoot(), reserve)
}

func customSearchFamily(name string) bool {
	return isIFixit(name) || strings.HasSuffix(strings.ToLower(name), repairedSuffix) || strings.HasPrefix(name, "gutenberg_") || strings.HasPrefix(name, "wikihow_") || strings.HasPrefix(name, "maps_")
}
func localSearchName(id search.Identity) string { return id.UUID + "-" + id.ChecksumMD5 }

func (a *app) prepareCollectionSearch(ctx context.Context, item downloadRequest) error {
	if a.searchDir == "" {
		return nil
	}
	if err := a.ensureSearchAssets(ctx); err != nil {
		return fmt.Errorf("Cannot prepare search; downloaded collections are safe: %w", err)
	}
	name := item.Name
	if completedRepair(filepath.Join(a.root, "content", "zim"), name) {
		name = repairedName(name)
	}
	if !customSearchFamily(name) {
		return nil
	}
	source := filepath.Join(a.root, "content", "zim", name)
	id, err := search.Identify(source)
	if err != nil {
		return err
	}
	// A publisher-version pack already covers this exact archive: no extraction.
	for _, pack := range []string{filepath.Join(a.searchRoot(), "custom")} {
		m, e := search.ReadManifest(pack)
		if e != nil {
			continue
		}
		for _, bound := range m.Archives {
			if bound.UUID == id.UUID && bound.Bytes == id.Bytes && bound.ChecksumMD5 == id.ChecksumMD5 {
				return nil
			}
		}
	}
	parent := filepath.Join(a.searchRoot(), "local")
	if err = os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	output := filepath.Join(parent, localSearchName(id))
	if m, e := search.ReadManifest(output); e == nil && len(m.Archives) == 1 && m.Archives[0].UUID == id.UUID && m.Archives[0].ChecksumMD5 == id.ChecksumMD5 && m.Archives[0].Bytes == id.Bytes {
		a.setJob(job{State: "indexing", Name: "Preparing search"})
		return search.Verify(ctx, output)
	}
	a.mu.Lock()
	reserve := uint64(a.effectiveReserveGiB()) * gibibyte
	a.mu.Unlock()
	free, err := availableSpace(parent)
	if err != nil || !spaceSufficient(free, collectionSearchWorkAllowance(name, id.Bytes), reserve) {
		return errors.New("Not enough working space to prepare search. Downloads are safe; free some space and continue")
	}
	identity, err := directoryIdentity(parent)
	if err != nil {
		return err
	}
	last := time.Time{}
	guard := func() error {
		if e := a.checkProcessingSpace(32 << 20); e != nil {
			return e
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if time.Since(last) < 100*time.Millisecond {
			return nil
		}
		last = time.Now()
		if e := a.checkLibraryRoot(); e != nil {
			return e
		}
		now, e := directoryIdentity(parent)
		if e != nil || !os.SameFile(identity, now) {
			return errors.New("Search drive changed")
		}
		free, e := availableSpace(parent)
		if e != nil || !spaceSufficient(free, 32<<20, reserve) {
			return errors.New("Search preparation stopped to protect your free-space reserve")
		}
		return nil
	}
	a.setJob(job{State: "indexing", Name: "Preparing search"})
	id.Collection = strings.TrimSuffix(name, filepath.Ext(name))
	label := id.Collection
	if isIFixit(item.Name) {
		label = "iFixit repair guides"
	}
	err = search.Build(ctx, output, id, label, func(emit func(search.Document) error) error {
		return zim.WalkHTML(ctx, source, func(path, title string, raw []byte) error {
			if e := guard(); e != nil {
				return e
			}
			docs, e := search.Extract(path, title, raw)
			if e != nil {
				return e
			}
			if len(docs) == 0 {
				docs = []search.Document{{Path: path, Title: title}}
			}
			for _, d := range docs {
				if e = emit(d); e != nil {
					return e
				}
			}
			return nil
		})
	}, guard)
	if err != nil {
		return fmt.Errorf("Could not prepare search; original and unfinished work retained: %w", err)
	}
	return a.checkLibraryRoot()
}

func (a *app) hybridLibrarySearch(w http.ResponseWriter, r *http.Request) {
	kind, topic := r.URL.Query().Get("kind"), r.URL.Query().Get("topic")
	if !validReadingFilters(kind, topic) {
		http.Error(w, "Invalid reading filter", 400)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	field := r.URL.Query().Get("field")
	if field == "" {
		field = "deep"
	}
	if query == "" || len(query) > 200 || len(strings.Fields(query)) > 20 {
		http.Error(w, "Enter up to 20 search words", 400)
		return
	}
	if field != "deep" && field != "title" && field != "all" && field != "author" && field != "subject" && field != "heading" && field != "text" {
		http.Error(w, "Invalid search field", 400)
		return
	}
	id := r.URL.Query().Get("id")
	if id != "" && !uuidPattern.MatchString(id) {
		http.Error(w, "Invalid collection", 400)
		return
	}
	if (field == "heading" || field == "text") && id == "" {
		http.Error(w, "Choose a collection for this search mode", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	items, err := a.localCollections(ctx)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	base, err := a.localReaderURL()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	files, err := libraryArchives(a.root)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	identities := map[string]search.Identity{}
	for _, name := range files {
		identity, e := search.Identify(name)
		if e == nil {
			identities[identity.UUID] = identity
		}
	}
	a.searchMu.Lock()
	defer a.searchMu.Unlock()
	if a.searchEngine == nil {
		a.searchEngine = search.NewLibrary()
	}
	results := []localSearchResult{}
	unavailable := []string{}
	skipped := []string{}
	attempted, searched, total := 0, 0, 0
	estimated := false
	var native []search.NativeCollection
	aliases := map[string]localCollection{}
	appendDocs := func(docs []search.Document, item localCollection) {
		for _, d := range docs {
			for _, part := range strings.Split(d.Path, "/") {
				if part == "." || part == ".." {
					return
				}
			}
			if d.Path == "" || strings.ContainsRune(d.Path, 0) {
				continue
			}
			u := url.URL{Path: "/content/" + item.Name + "/" + d.Path, Fragment: d.Anchor}
			results = append(results, localSearchResult{Title: d.Title, Collection: item.Title, URL: strings.TrimSuffix(base, "/") + u.String(), Snippet: d.Text, Authors: d.Authors, Subjects: d.Subjects})
		}
	}
	for _, item := range items {
		aliases[item.Name] = item
		if (id != "" && item.ID != id) || !matchesReadingFilters(item, kind, topic) {
			continue
		}
		attempted++
		if item.AddressConflict {
			unavailable = append(unavailable, item.Title)
			continue
		}
		if field == "deep" && item.IndexStatus == "yes" {
			native = append(native, search.NativeCollection{UUID: item.ID, Title: item.Title, Language: item.Language})
			continue
		}
		identity, ok := identities[item.ID]
		found := false
		if ok {
			packName := "catalog"
			if field == "deep" || field == "heading" || field == "text" {
				packName = "custom"
			}
			packs := []string{filepath.Join(a.searchRoot(), "local", localSearchName(identity)), filepath.Join(a.searchRoot(), packName), filepath.Join(a.searchDir, packName)}
			for _, pack := range packs {
				if a.searchDir == "" && pack == filepath.Join(a.searchDir, packName) {
					continue
				}
				n, docs, match, e := a.searchEngine.Query(ctx, pack, identity, query, field)
				if !match && e == nil {
					continue
				}
				found = true
				if e != nil {
					unavailable = append(unavailable, item.Title)
				} else {
					searched++
					total += n
					appendDocs(docs, item)
				}
				break
			}
		}
		if found {
			continue
		}
		if field == "deep" && item.IndexStatus != "no" {
			native = append(native, search.NativeCollection{UUID: item.ID, Title: item.Title, Language: item.Language})
			continue
		}
		if field == "title" || field == "all" {
			docs, failures, _ := a.searchTitles(ctx, base, query, []localCollection{item}, 0)
			results = append(results, docs...)
			unavailable = append(unavailable, failures...)
			if len(failures) == 0 {
				searched++
				total += len(docs)
				estimated = true
			}
			continue
		}
		skipped = append(skipped, item.Title)
	}
	if len(native) > 0 {
		n, docs, nativeSearched, e := search.NativeSearch(ctx, base, query, native)
		total += n
		estimated = true
		if e != nil {
			unavailable = append(unavailable, "Some collection searches failed")
		}
		searched += nativeSearched
		for _, d := range docs {
			if item, ok := aliases[d.Collection]; ok {
				appendDocs([]search.Document{d}, item)
			}
		}
	}
	// Interleave collection results rather than treating backend scores as equal.
	groups := map[string][]localSearchResult{}
	order := []string{}
	for _, item := range results {
		if _, ok := groups[item.Collection]; !ok {
			order = append(order, item.Collection)
		}
		groups[item.Collection] = append(groups[item.Collection], item)
	}
	balanced := []localSearchResult{}
	for row := 0; len(balanced) < 60; row++ {
		added := false
		for _, name := range order {
			if row < len(groups[name]) {
				balanced = append(balanced, groups[name][row])
				added = true
			}
		}
		if !added {
			break
		}
	}
	if len(balanced) > 60 {
		balanced = balanced[:60]
	}
	notice := ""
	if len(skipped) > 0 {
		notice = "Some collections need a matching search index. Titles remain searchable."
	}
	if len(unavailable) > 0 {
		notice += " Some searches could not finish; available results are shown."
	}
	writeJSON(w, map[string]any{"hybrid": true, "results": balanced, "total": total, "estimated": estimated, "notice": notice, "collectionsSearched": searched, "collectionsAttempted": attempted, "skipped": skipped, "unavailable": unavailable, "hasMore": false, "perCollectionLimit": 60, "titleOnly": 0})
}

func (a *app) markCustomSearch(items []localCollection) {
	files, err := libraryArchives(a.root)
	if err != nil {
		return
	}
	ids := map[string]search.Identity{}
	for _, file := range files {
		if id, e := search.Identify(file); e == nil {
			ids[id.UUID] = id
		}
	}
	var base []search.Manifest
	roots := []string{a.searchRoot()}
	if a.searchDir != "" {
		roots = append(roots, a.searchDir)
	}
	for _, root := range roots {
		for _, kind := range []string{"catalog", "custom"} {
			if m, e := search.ReadManifest(filepath.Join(root, kind)); e == nil {
				base = append(base, m)
			}
		}
	}
	for i, item := range items {
		id, ok := ids[item.ID]
		if !ok {
			continue
		}
		packs := append([]search.Manifest(nil), base...)
		if m, e := search.ReadManifest(filepath.Join(a.searchRoot(), "local", localSearchName(id))); e == nil {
			packs = append(packs, m)
		}
		for _, m := range packs {
			for _, bound := range m.Archives {
				if bound.UUID == id.UUID && bound.Bytes == id.Bytes && bound.ChecksumMD5 == id.ChecksumMD5 {
					items[i].IndexedTitles = true
					if m.BodyWords >= 0 && m.SourceType != "catalog-queues" {
						items[i].CustomText = true
					}
				}
			}
		}
	}
}

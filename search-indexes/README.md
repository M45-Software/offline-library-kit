# Prebuilt search indexes

These are derived search data, included at the operator's explicit request.
They contain word dictionaries, postings, titles and available source author and
subject metadata. They do not include ZIM archives, illustrations, or a copy of
the book/article bodies. The original archives still download from their publishers.
Source-derived data retains source attribution and applicable source terms;
the toolkit's MIT software license does not relicense it. See ATTRIBUTION.json.

The basic catalog covers 22,309,814 entries in 33 general collections and uses
397,658,071 binary bytes. Supplementary text search covers 467,247 pages/books in
15 collections and uses 285,264,079 bytes. The combined binary data is 682,922,150
bytes (about 682.9 MB), plus small manifests and notices. Native Kiwix full-text
indexes are reused for the other 18 collections instead of duplicated.

Parts are at most 64 MiB and stored with ordinary Git, without Git LFS. Search
reads them directly; no joined copy is required. Each manifest lists SHA-256
values for every part and the complete virtual file. UUID, size and stored ZIM
checksum bind each collection to its exact source. A binding is not a substitute
for verifying the downloaded archive. Missing or changed archives are excluded.

The iFixit binding describes an older local adaptation. The app does not rebind
it to new repaired files: it builds a compatible index after its own local repair.
The WikiMed binding is an older publisher version than the current default;
its native search remains usable with newer versions. All entries are general
reference collections; no personal equipment or regional overlay data
is included. Maps coverage here is HTML/navigation, not geographic tile search.

Custom text matching uses Unicode word AND matching across a page. It does not
promise phrase/proximity matching. Native stemming and counts differ; the app
balances collection results and labels estimated totals. New coverage notices
and unsupported/new archive versions require a compatible local custom index.

Developer validation (no downloads):

    cd desktop
    go run ./cmd/library-search -mode verify -dir ../search-indexes/catalog
    go run ./cmd/library-search -mode verify -dir ../search-indexes/custom

Only these finished parts and manifests belong here. Never add extraction
queues, sorting runs, partial builds, source symlinks or ZIMs.

# Fresh wikiHow index space measurement

Measured on 2026-11-07 using the desktop search builder from `c9f897b`, with
`GOMAXPROCS=2` and `GOMEMLIMIT=768MiB`. Output was written to a Linux internal
SSD. The source archive was read in place and preserved. No archive or generated
index is included in this report.

| Item | Bytes | Decimal size |
| --- | ---: | ---: |
| Source archive | 51,256,446,526 | 51.256 GB |
| Finished index binary | 49,930,680 | 49.93 MB |
| Index manifest | 1,019 | 0.001 MB |
| Finished index with manifest | 49,931,699 | 49.93 MB |
| Sampled peak allocated indexing files | 3,547,209,728 | 3.547 GB (3.30 GiB) |

The index contains 122,463 pages, 833,975 extracted passages, and 218,802 terms.
Extraction, building, and built-in verification took about 245 seconds on this
host; this is not a performance promise. A `water` text search matched the exact
archive identity and returned 39,079 pages. The index passed full SHA-256
verification. Source stored ZIM checksum checks and full SHA-256 before and after
agreed. The source SHA-256 local baseline is
`2345b7357c874b0d74b91e9c3a81e5a6c46b677a967e69f8beaca2e8a1d1fbf7`;
it is not a publisher-provided checksum.

Output files were sampled every 100 ms, including hidden extraction spools and
sorting runs. The peak excludes the source archive, bundled search packs,
compiler caches, logs, and directory metadata. It is a sampled lower bound;
brief growth between samples can be missed.

## Planning allowances

wikiHow now allows 4.5 GiB for a fresh index build and 64 MiB for its finished
index. These include at least 25% headroom above the observation, rounded upward.
Larger archive editions scale both allowances upward; smaller editions retain
the measured baseline. These estimates are not universal bounds for future
content changes. Every write continues to protect the free-space reserve.

The core and control panel share `desktop/search-space.json`. Selected collections'
finished index allowances accumulate; temporary build space is the largest
`peak - finished` allowance because the desktop app builds one index at a time.
Archives, repair overhead, bundled indexes, and the free-space reserve are
accounted for separately. Exact archive versions covered by the bundled pack
skip local index construction once their identity has been checked.

## Earlier Gutenberg attempts

The first world-history attempt produced an approximately 3.01 GB spool and then
stopped on a page exceeding the repair reader's 16 MiB bound. Indexing now accepts
long book pages within the existing 128 MiB decoded-cluster bound; the repair
reader retains its 16 MiB page limit. Synthetic regression tests cover both.

The subsequent world-history and technology measurements were interrupted before
completion. There are no validated finished-size or peak-size measurements for
Gutenberg from those attempts. The 1 GiB fallback retained at that point was
uncalibrated. Subsequent fresh builds completed all 12 curated subjects, including
a verified geography download, and replaced that fallback with measured subject
profiles; see [Gutenberg space measurements](gutenberg-space-measurement.md).

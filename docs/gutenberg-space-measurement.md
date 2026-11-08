# Fresh Gutenberg index space measurements

Measured on 2026-11-07 with the desktop search engine from `bb74b24` using
`GOMAXPROCS=2` and `GOMEMLIMIT=768MiB` per build. Two builds ran concurrently;
all generated files went to an internal Linux SSD. Each subject used a fresh
output directory, without reusing an existing index or bundled search pack.
Original archives were preserved. No archive or generated index is committed.

Sizes below use decimal GB/MB for observations and binary GiB/MiB for allowances.
The finished size includes the binary index and its manifest. The peak counts
allocated space, which can exceed logical file lengths.

| Subject / edition | Source GB | Finished MB | Sampled peak GB | Peak allowance GiB | Finished allowance MiB |
| --- | ---: | ---: | ---: | ---: | ---: |
| World history / 2025-12 | 40.254 | 52.41 | 7.547 | 9 | 64 |
| History of the Americas / 2026-03 | 10.197 | 16.70 | 2.512 | 3 | 64 |
| Local history of the Americas / 2026-03 | 9.888 | 13.84 | 1.752 | 2.5 | 64 |
| Geography / 2026-03 | 8.095 | 12.83 | 1.788 | 2.5 | 64 |
| Music / 2026-03 | 3.994 | 4.03 | 0.338 | 1 | 64 |
| Fine arts / 2026-03 | 22.753 | 7.92 | 0.735 | 1 | 64 |
| British literature / 2026-03 | 16.152 | 60.91 | 10.091 | 12 | 128 |
| American literature / 2026-03 | 16.022 | 51.92 | 7.518 | 9 | 64 |
| Juvenile literature / 2026-03 | 19.770 | 27.03 | 4.463 | 5.5 | 64 |
| Science / 2026-03 | 17.725 | 20.03 | 2.215 | 3 | 64 |
| Agriculture / 2026-03 | 4.533 | 5.74 | 0.590 | 1 | 64 |
| Technology / 2026-03 | 13.155 | 9.17 | 1.445 | 2 | 64 |

## Verification and limits

Each source passed its stored ZIM MD5 checksum and received a full SHA-256 local
baseline before indexing. The geography download also matched Kiwix's supplied
SHA-256 digest. After each build, file identity, size, modification time, archive
UUID and stored checksum were rechecked. These post-build checks are not a second
full source hash. Each finished index passed its SHA-256 verification, matched
its source archive identity and answered a text query with results. The exact
bytes, source hashes, document counts, queries and timings are recorded in
[gutenberg-space-measurements.json](gutenberg-space-measurements.json).

The monitor sampled every 100 ms and summed file lengths and allocated blocks
under each build's output directory, including hidden extraction spools and sort
runs. The peak excludes the original ZIM, compiler caches, logs, directory
metadata and unrelated simultaneous builds. Sampling can miss brief peaks, so
these observations are lower bounds. Timings are observations on this host, not
performance promises. Long books use the indexing reader's existing 128 MiB
decoded-cluster bound; the repair reader retains its 16 MiB HTML limit.

## Planning allowances

The core and control panel share per-subject profiles in
`desktop/search-space.json`, matched by archive filename prefix or curated catalog
ID. Peak allowances add at least 25% to the sampled allocated peak, round up to
512 MiB and have a 1 GiB minimum. Finished allowances add at least 25%, round up
to 64 MiB and have a 64 MiB minimum. Larger source editions scale both allowances
upward; smaller editions keep the measured baseline. Unknown Gutenberg subjects
use the largest observed subject allowance with the largest source size as its
scaling baseline. Content changes can exceed size-based projections; these are
planning estimates, not universal upper bounds.

Selected finished-index allowances accumulate. Temporary indexing workspace is
the largest `peak - finished` allowance because the desktop app serializes index
builds even with simultaneous downloads. Source archives, iFixit repair copies,
bundled index packs and free-space reserves are accounted for separately. An
exact archive version covered by a verified bundled search pack skips its local
build. Direct destinations need peak build room; staged destinations need room
for the finished library while the staging drive needs peak room. Runtime write
checks continue protecting the reserve.

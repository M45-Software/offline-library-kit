# Fresh iFixit repair and index space measurement

Measured on 2026-11-07 using the repair and search APIs from commit
`0dcafeb58a68c4c2d2a05d3cb11875c8c7562ee0`, with two repair workers,
`GOMAXPROCS=2`, and `GOMEMLIMIT=768MiB`. Output was written to an internal
Linux SSD. The original was read in place and preserved. No archive or binary
is included in this report.

| Item | Bytes | Decimal size |
| --- | ---: | ---: |
| Original archive | 3,570,695,757 | 3.571 GB |
| Fresh repaired archive | 3,599,805,672 | 3.600 GB |
| Finished index binary | 25,588,662 | 25.59 MB |
| Index manifest | 1,019 | 0.001 MB |
| Finished repaired archive and index | 3,625,395,353 | 3.625 GB |
| Sampled peak allocated output/work files | 5,768,527,872 | 5.769 GB |
| Peak indexing allocation beyond repaired archive allocation | 2,168,713,216 | 2.169 GB (2.02 GiB) |

The index covers 253,845 pages from 1,454,472 extracted passages, with 422,194
terms. Repair took about 80 seconds including checksum readback; extraction,
index building, and its built-in verification took about 138 seconds. These
are observations on one host, not performance promises.

## What was verified

- The repairer checked the original's stored ZIM checksum while copying.
- The repaired archive passed independent full checksum readback.
- Original SHA-256 before and after matched:
  `9bb5f3408707fd0300c6ef47a425c3217187f144cd9154a928183816aa72eead`.
  This is a local baseline, not a publisher-provided checksum.
- The finished index passed its SHA-256 verifier and matched the repaired
  archive's identity. A `battery` text query returned 40,083 matching pages.
- Repair checked 253,845 pages, changed 15,928 pages, repaired 2,670 links,
  and added 14,295 coverage notices. It did not recover missing material.

## Interpreting space requirements

Output files were sampled every 100 ms, summing both logical file lengths and
allocated filesystem blocks, including hidden extraction spools and sorting
runs. The sampled peak is a lower bound; brief growth between samples and
filesystem directory metadata are not included. Compiler caches, executable,
logs, and the source archive were outside the measured output tree.

Keeping the original on the build drive adds its 3.571 GB to the peak: about
9.34 GB total. Adding the bundled search assets (682,970,668 bytes) brings this
to about 10.02 GB, before the free-space reserve. A staged destination receives
the repaired archive and finished indexes, about 4.31 GB including the bundled
assets. A direct build retains the original, even after temporary work is removed.

The former 1 GiB search workspace allowance was insufficient. iFixit now uses
4 GiB for index preparation, with headroom above this observation. The UI keeps
its conservative finished-index allowance separate from the build allowance;
other custom collection families retain their existing estimates. These are
planning estimates for changing archives, not universal upper bounds. Per-write
free-space guards remain necessary. Recommend staging when the finished library
fits the destination, the direct build does not, and separate storage has enough
room for the build plus its reserve.

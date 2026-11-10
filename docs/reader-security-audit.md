# Native reader security audit

Checked on 2026-11-10 for the Kiwix readers selected by
`scripts/ci-fetch-fixtures.py`. The Go/npm/Python audits added in PR #1 do not
patch or fully scan these native binaries.

The selected readers are still the newest archives in the
[publisher's tools directory](https://download.kiwix.org/release/kiwix-tools/):
3.8.2 on Linux and both Macs, and 3.8.1 on Windows. Their embedded dependencies
nevertheless need a rebuild review. This audit does not establish an exploitable
vulnerability in the application, clear the binaries for distribution, or apply
native fixes. No reader binaries or source archives were added to the repo.

## Artifact evidence

All four archives matched the publisher's current MD5 and the repository's
recorded SHA-256 baseline. MD5 is not strong authenticity verification; these
SHA-256 values are local baselines, not publisher signatures. The
[inventory](reader-native-inventory.json) records archive checksums and extracted
file hashes, including the Windows ICU DLLs.

- Linux: ran the downloaded reader's `--version` on Linux x86-64. `file` and
  `ldd` identify it as a stripped, statically linked executable. Updating host
  shared-library packages does not replace its embedded copies.
- Windows: ran `--version` under Wine on Linux using an isolated temporary
  prefix. This is version inspection, not native Windows validation. The
  executable imports the bundled ICU DLLs and host-supplied Microsoft runtimes.
- Both Macs: inspected printable strings in each verified Mach-O executable.
  They contain versions consistent with the candidate Linux/Mac source record,
  including curl 8.4.0, zlib 1.3.1, Xapian 1.4.23, ICU 73.2, zstd 1.5.7 and
  liblzma 5.2.6. Neither binary was executed on macOS; microhttpd and pugixml
  versions were not freshly verified there.

Self-reported versions and strings cannot prove exact source, backported fixes,
build flags, linkage, or a complete software bill of materials. Candidate-source
observations below retain that limitation; see the
[source review](reader-source-review.md) for the underlying provenance evidence.

| Component | Linux version report | Windows version report |
| --- | --- | --- |
| Kiwix tools | 3.8.2 | 3.8.1 |
| libkiwix | 14.2.0 | 14.1.1 |
| libzim | 9.5.0 | 9.4.0 |
| curl | 8.4.0 | 8.4.0 |
| microhttpd | 0.9.76 | 0.9.76 |
| zlib | 1.3.1 | 1.3.1 |
| ICU | 73.2.0 | 74.1.0 |
| Xapian | 1.4.23 | 1.4.23 |
| pugixml | 1.15.0 | 1.15.0 |
| zstd | 1.5.7 | 1.5.5 |
| liblzma | 5.2.6 | 5.2.11 |

## Advisory triage

### Rebuild and verify

- **curl 8.4.0:** its version matches numerous entries in the
  [vendor advisory feed](https://curl.se/docs/vuln-8.4.0.json). This is a
  candidate list, not a count of exploitable application flaws. For example,
  [CVE-2024-7264](https://curl.se/docs/CVE-2024-7264.html) depends on the TLS
  backend and certificate parsing; those build details need verification.
  Rebuild with a currently patched curl, such as the vendor's
  [8.23.0 release](https://curl.se/changes.html), or document equivalent
  backports and disabled features. The desktop's catalog and download requests
  use Go's HTTP client; the reader's libcurl is a separate dependency.
- **zlib 1.3.1:** the vendor's
  [1.4 release notes](https://zlib.net/zlib.html) describe fixes affecting this
  version line, including dictionary/synchronization-related memory problems.
  The affected function sequences were not reproduced in Kiwix. Rebuild and
  test with 1.4 or verified backports; do not substitute 1.3.2 as a blanket fix
  because the 1.4 notes also identify issues in that release.
- **microhttpd 0.9.76:** it is the reader's HTTP server, so retain it in the
  native patch review. CVE-2025-59777 and CVE-2025-62689 refer to the
  [upstream fix](https://git.gnunet.org/libmicrohttpd.git/commit/?id=ff13abc1c1d7d2b30d69d5c0bd4a237e1801c50b);
  [Debian's build analysis](https://security-tracker.debian.org/tracker/CVE-2025-59777)
  notes that the relevant code requires `HAVE_EXPERIMENTAL`. The archive's flag
  and patch status were not established, so these remain conditional findings.
  Verify the flag and fix when updating to a supported
  [GNU release](https://www.gnu.org/software/libmicrohttpd/).
- **Windows zstd 1.5.5 and pugixml 1.15.0:** consider updating during the same
  rebuild to [zstd 1.5.7](https://github.com/meta/zstd/releases/tag/v1.5.7) and
  [pugixml 1.16](https://github.com/zeux/pugixml/releases/tag/v1.16). These are
  maintenance candidates, not confirmed security findings. No published
  advisories were listed on the checked upstream
  [zstd](https://github.com/meta/zstd/security) or
  [pugixml](https://github.com/zeux/pugixml/security) security pages. Absence of
  an advisory is not a security clearance.

### Version matches with unmet or unverified prerequisites

- **curl CVE-2025-0725:** the
  [vendor advisory](https://curl.se/docs/CVE-2025-0725.html) requires zlib
  1.2.0.3 or older. The Linux and Windows readers report zlib 1.3.1, so that
  prerequisite is absent in those reports.
- **XZ/liblzma:**
  [GHSA-5qpq-xqfv-j9pg](https://github.com/tukaani-project/xz/security/advisories/GHSA-5qpq-xqfv-j9pg)
  excludes `.xz` stream decoders; the candidate libzim
  [9.5.0](https://github.com/openzim/libzim/blob/9.5.0/src/compression.cpp) and
  [9.4.0](https://github.com/openzim/libzim/blob/9.4.0/src/compression.cpp) reader
  implementations initialize `lzma_stream_decoder`.
  [CVE-2026-34743](https://github.com/tukaani-project/xz/security/advisories/GHSA-x872-m794-cxhv)
  needs a decoded index followed by `lzma_index_append`; that sequence is not
  in the inspected compression implementations. No affected reader path was
  demonstrated. Updating to 5.8.4 or later during a rebuild still removes
  reliance on the old branches; the vendor states that those branches receive
  Git fixes without new release tarballs.
- **Xapian CVE-2026-77643:** the
  [upstream advisory](https://trac.xapian.org/wiki/SecurityFixes/2018-07-02)
  requires empty start/end highlighting strings and short input. Candidate
  libzim [9.5.0](https://github.com/openzim/libzim/blob/9.5.0/src/search_iterator.cpp)
  and [9.4.0](https://github.com/openzim/libzim/blob/9.4.0/src/search_iterator.cpp)
  use the snippet method's default highlighting, documented as nonempty in
  [Xapian's API](https://xapian.org/docs/apidoc/html/classXapian_1_1MSet.html).
  That checked path does not meet the prerequisite. The vendor's 1.4.32 fix
  remains a suitable minimum for the same release series in a rebuilt reader.
- **ICU CVE-2025-5222:** the
  [CNA description](https://access.redhat.com/security/cve/cve-2025-5222)
  concerns the `genrb` resource compiler. The extracted reader payloads do not
  contain that executable, so this is not evidence of a bundled-reader runtime
  flaw. Build-tool dependencies need separate review when rebuilding ICU.

The checked upstream [tools](https://github.com/kiwix/kiwix-tools/security),
[libkiwix](https://github.com/kiwix/libkiwix/security), and
[libzim](https://github.com/openzim/libzim/security) pages listed no published
security advisories. This does not exclude undisclosed issues, older CVEs
documented elsewhere, or security-relevant fixes in their change logs. This is
a targeted advisory review, not an exhaustive audit of every native component.

## Existing exposure limits

The desktop starts Kiwix with `--address=127.0.0.1` and `--blockexternal` in
`desktop/main.go`; the supplementary search reader uses the same options in
`desktop/internal/librarysearch/expanded.go`. The Electron shell limits reader
navigation to loopback HTTP. These controls reduce exposure but do not patch a
native parser processing a selected archive or protect it from every local
process. No new application protection or native fix is claimed by this audit.

## Work required before treating the reader update as fixed

1. Obtain newer publisher builds or build all four reader targets from pinned
   sources with a reviewed dependency/patch manifest. Start with the curl,
   zlib and microhttpd checks above; verify ICU and the remaining transitive
   and platform libraries as part of that build.
2. Create a new, versioned build-input lock. Preserve the existing experimental
   reconstruction lock as historical evidence; changing its version labels
   would not update any packaged binary.
3. Record compiler and native-library versions, build options, checksums and
   patch evidence for the actual outputs. Recheck conditional advisory paths.
4. Run upstream tests, malformed-input checks, and native packaged-app
   reading/search/navigation and signing tests on Linux, Windows and both Mac
   architectures. Cross-building the Go core alone does not validate readers.
5. Update reader URLs, sizes and verified checksum baselines only after those
   binaries exist and pass validation. Complete the existing source/license
   review before distributing them.

The inspected download artifacts stayed in temporary storage. No library ZIMs,
personal files, or replacement reader binaries were published by this audit.

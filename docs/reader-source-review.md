# Reader source review

This is an evidence record for preparing a future desktop distribution, not a
license clearance or a claim that all corresponding source has been identified.
The reader archives have only been used in local development builds and CI.

See the [native reader security audit](reader-security-audit.md) for the
2026-11-10 artifact inventory, advisory triage and outstanding rebuild work.

The one-command preview builder currently selects these Kiwix tools archives:

| Build target | Reader archive | Candidate upstream tag and commit |
| --- | --- | --- |
| Linux x86-64, Mac Intel, Mac Apple Silicon | 3.8.2 | [3.8.2](https://github.com/kiwix/kiwix-tools/tree/f76e38bdc06b86b73ba8e84a5de234a0d6c3fa3d) (`f76e38bdc06b86b73ba8e84a5de234a0d6c3fa3d`) |
| Windows x86-64 | 3.8.1 | [3.8.1](https://github.com/kiwix/kiwix-tools/tree/2308b02504dee70157a26744a793d51bb8eb5f14) (`2308b02504dee70157a26744a793d51bb8eb5f14`) |

The candidate commits were resolved from the upstream tags on 2026-11-05.
Their version numbers match the archive names; that alone does **not** prove
that those trees and any particular dependency sources produced these binaries.
The upstream [3.8.2 build configuration](https://github.com/kiwix/kiwix-tools/blob/f76e38bdc06b86b73ba8e84a5de234a0d6c3fa3d/meson.build)
and [3.8.1 build configuration](https://github.com/kiwix/kiwix-tools/blob/2308b02504dee70157a26744a793d51bb8eb5f14/meson.build)
specify libkiwix, libzim and docopt dependencies, but version ranges are not a
complete list of the exact transitive inputs or patches used in a release.

On 2026-11-05 the downloaded Linux x86-64 3.8.2 reader reported libkiwix 14.2.0,
libzim 9.5.0, Xapian 1.4.23, curl 8.4.0, microhttpd 0.9.76, zlib 1.3.1, ICU
73.2.0, pugixml 1.15.0, zstd 1.5.7 and liblzma 5.2.6 via `--version`. That is
self-reported version information, not proof of exact source trees, patches,
linkage, licenses or build options. Do not assume the Windows or Mac binaries
have the same inputs; native one-command builds record their own reports in
`build-info.json` when available. Cross-target builds do not run the reader.

## Source archive and release-build evidence

On 2026-11-05, the publisher-hosted source archives were downloaded for audit.
Each matched the publisher's MD5; the SHA-256 values below are local baselines,
not publisher signatures:

| Source archive | Publisher MD5 | Local SHA-256 |
| --- | --- | --- |
| [kiwix-tools-3.8.2.tar.xz](https://download.kiwix.org/release/kiwix-tools/kiwix-tools-3.8.2.tar.xz) | `b106dc7d1bba1929c4e82764e6bf9fe1` | `1238f85984fb5c98aff646f6cec22b5bcf7c832c31b8a24df07c34f436c929f9` |
| [kiwix-tools-3.8.1.tar.xz](https://download.kiwix.org/release/kiwix-tools/kiwix-tools-3.8.1.tar.xz) | `187c299d63cd458accc27d5827c18751` | `dd769c9bd3d75b59ad9e451b128187b128da6a10b1241bb2d0325fe4aafe51a3` |

For each archive, all 48 regular files matched the Git blob hashes of the
corresponding tag above; neither archive contained a `subprojects` directory.
This checks the files present, not that the archive includes every file in the
Git repository, and does not establish the binary's provenance. MD5 is not a
strong authenticity check.

Upstream Kiwix Build's [r_100 release revision](https://github.com/kiwix/kiwix-build/blob/8062f23fb42454c9e6db319abe71d2add7263de7/kiwixbuild/versions.py)
selects tools 3.8.2, libkiwix 14.2.0 and libzim 9.5.0; its
[release run](https://github.com/kiwix/kiwix-build/actions/runs/22567605021)
included successful Linux and Mac jobs and a failed Windows static job. The
[r_91 release revision](https://github.com/kiwix/kiwix-build/blob/6f3a27d3988c424b5dbb82a29ff508aad0055211/kiwixbuild/versions.py)
selects tools 3.8.1, libkiwix 14.1.1 and libzim 9.4.0; its
[release run](https://github.com/kiwix/kiwix-build/actions/runs/19848940976)
reported successful Windows static, Linux and Mac jobs. These are plausible
build leads, not proof that the downloaded binaries came from those jobs.
Their historical job logs returned HTTP 410 when checked. The general version
tables alone are not the complete recipes: for example, r_100 lists Xapian
1.4.26 while the Linux reader reports 1.4.23; r_91 lists ICU 73.2 and zstd
1.5.2 while the Windows reader reports ICU 74.1.0 and zstd 1.5.5.

## Recipe evidence for reported dependency versions

A closer inspection of the same upstream revisions found explicit source
selections that agree with those binary reports:

- The r_100 [non-Windows Xapian recipe](https://github.com/kiwix/kiwix-build/blob/8062f23fb42454c9e6db319abe71d2add7263de7/kiwixbuild/dependencies/xapian.py)
  specifies `xapian-core-1.4.23.tar.xz` with SHA-256
  `30d3518172084f310dab86d262b512718a7f9a13635aaa1a188e61dc26b2288c`.
- The r_91 [Windows ICU recipe](https://github.com/kiwix/kiwix-build/blob/6f3a27d3988c424b5dbb82a29ff508aad0055211/kiwixbuild/dependencies/icu4c.py)
  selects Unicode's prebuilt ICU 74.1 Windows package; that recipe does not
  specify a SHA-256 for the package.
- The r_91 [Zstandard recipe](https://github.com/kiwix/kiwix-build/blob/6f3a27d3988c424b5dbb82a29ff508aad0055211/kiwixbuild/dependencies/zstd.py)
  selects source version 1.5.5 with SHA-256
  `98e9c3d949d1b924e28e01eccb7deed865eefebf25c2f21c702e5cd5b63b85e1`.

On 2026-11-05, downloads of the [Xapian archive](https://dev.kiwix.org/kiwix-build/xapian-core-1.4.23.tar.xz)
and [Zstandard archive](https://github.com/facebook/zstd/archive/refs/tags/v1.5.5.tar.gz)
matched those recipe hashes. These hashes originate in the candidate Kiwix
Build recipes; they are not independent publisher signatures. The matching
recipes explain why the general version tables alone were misleading, but do
not establish which recipes, archives, patches, or build options produced the
shipped binaries. In particular, the ICU package is prebuilt and its own source
and build inputs require separate review. Do not treat these leads as a complete
corresponding-source manifest.

## Windows ICU package and candidate source

On 2026-11-05, Unicode's [ICU 74.1 Windows package](https://github.com/unicode-org/icu/releases/download/release-74-1/icu4c-74_1-Win64-MSVC2022.zip)
was downloaded and its five `bin64` DLLs were compared with the five ICU DLLs
in the Kiwix Windows tools 3.8.1 archive. All five SHA-256 hashes matched
byte for byte. The package's locally calculated SHA-256 was
`9cb6a48786dbd53e5f529cacdbf26d56064cd8df1d622116c5cbaad71e0fd3e3`;
this local hash is a baseline, not a publisher checksum.

The package's detached signature verified cryptographically using key
`A388409F41D489C776CE75DF554C6CE339D10995` from ICU's
[release-tagged key list](https://github.com/unicode-org/icu/blob/release-74-1/KEYS).
The key was obtained from the project over HTTPS and was not independently
certified in the isolated keyring; GnuPG reported its trust as unknown.

Unicode's [ICU 74.1 source archive](https://github.com/unicode-org/icu/releases/download/release-74-1/icu4c-74_1-src.tgz)
had local SHA-256 `86ce8e60681972e60e4dcb2490c697463fcec60dd400a5f9bffba26d0b52b8d0`.
Its detached signature was cryptographically valid with key
`3DA35301A7C330257B8755754058F67406EAA6AB` from the same key list, but GnuPG
reported that key expired; the key was not independently certified in this
check. The signatures and matching DLLs strengthen the identity evidence, but
neither proves that this exact source archive and its build inputs produced the
prebuilt DLLs, or that the remaining Kiwix dependencies have been identified.
The project's current key list was also checked on 2026-11-05 and still
reported the source-signing key expired; this is not an independent trust check.

The source archive was compared with the untruncated Git tree at ICU release tag
[`release-74-1`](https://github.com/unicode-org/icu/tree/9edac7b78327a1cb58db29e2714b15f9fa14e4d7),
mapping archive paths under `icu/` to repository paths under `icu4c/`. Of 2,647
regular archive files, 2,644 matched the corresponding Git blob hashes. The
remaining three require different treatment:

- `icu/LICENSE` is a regular file in the archive; the tag has a symlink at
  `icu4c/LICENSE` to the repository-root license. The archive file's bytes match
  that target license.
- `icu/source/test/cintltst/cldrtest.c` differs from the tagged file. The archive
  lacks two `kxv` known-issue test exceptions present in the tag. Its local
  SHA-256 is `c5be889b820ba20020cf05c9cf021a3a7df609f2e4d06fe60d855c8ff8e16b06`;
  the tagged file's is `76ac76fc9c3d79471c08f6f4b6b5072e606661fe4c38230ef9638218ec0b20bb`.
- `icu/source/data/in/icudt74l.dat` is in the archive but not at the mapped path
  in the tag; the archive file is 30,783,664 bytes, with local SHA-256
  `bdbdad8d28c7d178e9506a75614e070a2a3310b89618f1c753349906a4aad454`.
  A byte-for-byte scan of the Kiwix Windows archive's `icudt74.dll` found the
  complete data file starting at byte offset 1,024. That DLL has local SHA-256
  `ce6f56b89f3c7163a166210977642559c87033b579ff129c61443de57fbd771b` and
  also matched the copy in Unicode's 74.1 Windows package as described above.

This compares the regular files present in the archive, not every repository
file, and does not establish how the prebuilt ICU package was made. Preserve
both archive and tag as distinct candidate sources rather than treating them as
interchangeable.

## Candidate libkiwix and libzim sources

The readers report libkiwix 14.2.0 and libzim 9.5.0 on Linux and both Macs,
and libkiwix 14.1.1 and libzim 9.4.0 on Windows. On 2026-11-05 the following
publisher-hosted source archives were downloaded for review. Each matched its
publisher MD5. The SHA-256 values are locally calculated baselines, not
publisher signatures.

| Candidate source | Publisher MD5 | Local SHA-256 | Regular files matching candidate tag |
| --- | --- | --- | ---: |
| [libkiwix 14.2.0](https://download.kiwix.org/release/libkiwix/libkiwix-14.2.0.tar.xz) | `19967b528b9eb6df418ea9dabb444b94` | `6431e3dd31edcfc9a1cf2930a266616e845002882211adfe4c1dab9c28af540b` | 288 |
| [libkiwix 14.1.1](https://download.kiwix.org/release/libkiwix/libkiwix-14.1.1.tar.xz) | `b9c862a8911caeed980edef1575735d0` | `e232f42bba33561493e2d7318c3be60d8508e83a8891a8358135519dedc5ff5a` | 287 |
| [libzim 9.5.0](https://download.openzim.org/release/libzim/libzim-9.5.0.tar.xz) | `9d963007cfba2d5c309812adf48f801d` | `41fdcb748b11f28cdc11cb99c99d9ef462b445fbe354ada2bbe700b31f553819` | 245 |
| [libzim 9.4.0](https://download.openzim.org/release/libzim/libzim-9.4.0.tar.xz) | `d7cb45cff5b8d08439f8f2502e794aae` | `7fa374f4714b23c43afa3fb406d7e21c483d77e8218895e1408e2f037969b6ea` | 245 |

The candidate tags resolve respectively to [libkiwix 14.2.0](https://github.com/kiwix/libkiwix/tree/30adab733771835f25697e4f2ea18dc2eec4e92d),
[libkiwix 14.1.1](https://github.com/kiwix/libkiwix/tree/f82bfc068f3c0bed7a050f0d1adc5e8c7b8f1d5c),
[libzim 9.5.0](https://github.com/openzim/libzim/tree/0e95c45ec5ebd758784060111ea5a80a08c3f9c7), and
[libzim 9.4.0](https://github.com/openzim/libzim/tree/8ef511ba0de6a8180d79fbc3c7edcfec4202ae5e).
Every regular archive file matched the Git blob hash at its corresponding tag;
this does not show that all repository files were included, or that the binaries
were built from these exact archives. Each archive contains a `COPYING` file and
`subprojects` metadata. These archives are not a complete transitive source
bundle, and MD5 is not a strong authenticity check.

## Experimental Linux source build

On 2026-11-05, an isolated Linux x86-64 build used the candidate Kiwix Build
`r_100` revision above, its `native_static` configuration and release versions.
The tools, libkiwix and libzim checkouts matched the respective candidate
commits above; their tracked files had no local changes after the build. The
build used one CPU, disabled automatic system package installation, and ran
Meson tests sequentially. The upstream `SKIP_BIG_MEMORY_TEST=1` switch skipped
two libzim large-memory cases to stay within a 2 GiB memory limit. Libzim
reported 31 passing test executables and libkiwix reported 21; the skip messages
are inside libzim's passing test output. The Kiwix tools project reported no
tests in its Meson configuration. Temporary test files were on exFAT; this is
not a substitute for testing all filesystem behavior on native Linux storage.

The final link initially failed because the host linker emitted glibc static
linking warnings and the tools build treated warnings as errors. A local retry
set the tools build's Meson `werror` option to `false` and produced a reader;
the warnings remained visible. This setting also affects compiler warnings,
not only those specific linker warnings. The host used Ubuntu glibc 2.39 and
system OpenSSL 3.0.13 static libraries, so this is not evidence of portability
to other Linux systems or of a complete corresponding-source bundle. The
resulting experimental `kiwix-serve` SHA-256 was
`6b617ed1e10d6259cf6a2416f6f422995424b2772644dcea363ddf241d7f6b03`.
Its self-reported versions included tools 3.8.2, libkiwix 14.2.0, libzim 9.5.0
and Xapian 1.4.23. A temporary copy of the Linux desktop preview with that reader
substituted passed the packaged window, offline search, homepage, manual, reader,
safe-close and library-identity smoke test with a small ZIM fixture. The test
copy's original build metadata did not describe the substituted reader.

A subsequent local rebuild kept `werror=true` and added the C++ linker option
`-Wl,--no-fatal-warnings`. It emitted the same glibc warnings, retained
compiler warnings as errors, and produced the same reader SHA-256 as the smoke
tested binary. This option makes **all** linker warnings nonfatal; a release
build needs a more selective policy and a resolution of the compatibility
warnings. It does not suppress genuine linker errors.

The same reader hash later passed the packaged Linux app smoke test in a network
namespace with loopback as its only interface and no external routes. The test
used two verified ZIM fixtures prepared before isolation and exercised offline
search and reading, the homepage and manual, reserve persistence, safe close,
library identity and a real reader-address collision. The window and Electron
runtime came from the existing local preview; the current core and source-built
reader were supplied as explicit overrides. This does not broaden the source,
portability or upstream large-memory test coverage described above.

The [local archive inventory](reader-source-inputs-linux.tsv) records sizes
and locally computed hashes for the 18 files found in this build's archive
directory, including patches and test data. It is an inventory, not an
independent authenticity check or a complete source manifest: Meson-fetched
subprojects, Git checkouts and host-provided libraries need separate records.
The host packages included `libc6-dev` version `2.39-0ubuntu8.9` (source
`glibc`), `libssl-dev` version `3.0.13-0ubuntu3.16` (source `openssl`), and
`libstdc++-13-dev` version `13.3.0-6ubuntu2~24.04.1` (source `gcc-13`).
Their package versions identify candidates, not a collected source bundle.

### Host and test-source candidates

On 2026-11-06 UTC, the then-current Ubuntu [noble-updates InRelease](https://archive.ubuntu.com/ubuntu/dists/noble-updates/InRelease)
was verified with the locally installed Ubuntu archive keyring. `gpgv` reported
a good signature from archive signing key
`F6ECB3762474EDA9D21B7022871920D1991BC93C`. Its SHA-256 was
`e117da3cd0ed9d361feab51ad91ecdd07561fb2211d42ba27529cf504e21558b`;
the signed SHA-256 for `main/source/Sources.xz` was
`6caffd8b50d22c13bab24dae045784e2e6069cdbcc8883b599f05a92aa24ce3f`.
The downloaded source index matched that hash. It listed the following exact
descriptors, whose filename, size and SHA-256 matched the downloaded files:

| Candidate descriptor | SHA-256 |
| --- | --- |
| [OpenSSL 3.0.13-0ubuntu3.16](https://archive.ubuntu.com/ubuntu/pool/main/o/openssl/openssl_3.0.13-0ubuntu3.16.dsc) | `7d1e552a6f519836c352407e521bafdda7552ec6d1ba242968c35fc0e17f47b6` |
| [glibc 2.39-0ubuntu8.9](https://archive.ubuntu.com/ubuntu/pool/main/g/glibc/glibc_2.39-0ubuntu8.9.dsc) | `1b73a89e0910eb3e5920cde639ac7eb606256d8ee809688ff9b5912b67002711` |
| [GCC 13.3.0-6ubuntu2~24.04.1](https://archive.ubuntu.com/ubuntu/pool/main/g/gcc-13/gcc-13_13.3.0-6ubuntu2~24.04.1.dsc) | `86b4012c312ac13e3e092877719a62a5b5dbab082ae7e9680780a25c6a13ddc6` |

The source archives and Debian patch archives named in those descriptors were
downloaded and matched their listed sizes and SHA-256 values. The descriptors'
individual developer signatures could not be checked with the installed archive
keyring; the signature evidence above is for the archive index chain. The host
`libcrypto.a`, `libc.a` and `libstdc++.a` also matched their locally installed
package MD5 records. Those local records and MD5 do not independently prove
binary authenticity or reproducibility from the candidate sources.

The build's Meson test dependencies included GoogleTest 1.14.0 and 1.8.1 plus
their respective WrapDB patches. All four cached files matched the SHA-256 values
declared in the [libzim wrap](https://github.com/openzim/libzim/blob/0e95c45ec5ebd758784060111ea5a80a08c3f9c7/subprojects/gtest.wrap)
and [libkiwix wrap](https://github.com/kiwix/libkiwix/blob/30adab733771835f25697e4f2ea18dc2eec4e92d/subprojects/gtest.wrap).
The wrap hashes are project-supplied checks, not independent publisher signatures.
These are test-build inputs; this observation alone does not establish whether
any particular component is linked into the shipped reader.

A local candidate-input folder contains the verified Ubuntu files and index,
the Kiwix build archives and patches, Git archives for the four pinned projects,
and the cached test subprojects. Its local `SHA256SUMS` passed verification. It
includes upstream ZIM test fixtures, some deliberately invalid, but no user's
library collections or application binaries. This is an evidence collection,
not a complete corresponding-source bundle or authorization to redistribute.

### Experimental reader link trace

A local relink using GNU linker's `-t` option produced the same reader SHA-256
as the binary tested above. The trace identified 32 distinct files opened by the
linker: the reader's compiled object, 14 archives from the Kiwix build, and 17
host files. The Kiwix-built archives include libkiwix, libzim, compression,
Xapian, ICU, curl, microhttpd, pugixml, UUID and docopt components. The host
files mapped to four installed packages: `libc6-dev`, `libssl-dev`,
`libstdc++-13-dev` and `libgcc-13-dev`. Their recorded source packages are
glibc, OpenSSL and GCC respectively, matching the candidate source versions
above. All 17 host files matched their local package MD5 records; this is a
local integrity comparison, not independent authentication.

The candidate folder's `LINK-INPUTS.json` records paths and local SHA-256 values
for these files. The trace reports files opened by this link, not every archive
member actually used, all files read during compilation, possible runtime
modules, or a complete license and source inventory. The relink retained the
nonfatal-linker-warning setting described above; matching output does not resolve
the glibc compatibility warnings or establish cross-platform portability.

For notice review, a local filename-based scan of 32 captured source, patch and
test archives found 60 possible notice files or links. A separate scan inside
the nested GCC source archive found six more candidate paths under its root,
libgcc and libstdc++ trees, including `COPYING.RUNTIME`; GCC describes its
runtime licensing in the [libstdc++ manual](https://gcc.gnu.org/onlinedocs/libstdc++/manual/license.html).
The local inventories record paths and hashes, not license conclusions. They do
not establish which archive members are linked, scan every nested archive or
recognize notices with other filenames. Archives without a matching filename
must not be treated as having no notices. Platform-specific inputs and the
complete corresponding-source and license review remain open.

### Offline reconstruction and path normalization

Using the candidate inputs and an adapter for the pinned builder, a second
reader was built in a network namespace with only loopback available. It used
the host compiler, static libraries and Python build tools, so this was not an
independent reconstruction of the complete build environment. libzim reported
31 passing test executables and libkiwix 21; two libzim big-memory cases were
skipped to respect the 2 GiB limit. The rebuilt reader's SHA-256 was
`a96d6715bc197c12021fa1d767bc63085dffa33afb95a07a9573b831ddf706bf`,
different from the first source-built reader's
`6b617ed1e10d6259cf6a2416f6f422995424b2772644dcea363ddf241d7f6b03`.
The clean builds were therefore not byte-identical.

Thirteen of their 14 build-produced static libraries matched. The remaining
library differed in three objects containing absolute build paths. Recompiling
only those objects with each build path mapped to a common prefix, replacing
them in temporary archive copies, and relinking produced identical readers with
SHA-256 `44672e42e3f68c889921615bb4072b50227ac415d24eb4e9ab5adc5a945aa976`.
That controlled partial rebuild alone did not establish that two clean, fully
prefix-mapped builds were reproducible. Four static glibc linker warnings were
allowed; linker errors remained fatal.

Both the original source-built reader and the normalized reader passed the
network-isolated Linux GUI smoke test after correcting the harness to request
the Chromium sandbox explicitly. Earlier smoke runs did not establish that
sandbox path because Playwright had silently added `--no-sandbox`. Local
evidence records the reconstruction and partial relink; it is not a complete
source or license inventory.

### Two clean prefix-mapped builds

Two further builds started from the captured candidate inputs in separate
directories, each in a network namespace with loopback only. Both used the
same host compiler, static libraries and Python tools, with each build root
mapped to `/build/olk` using `-ffile-prefix-map` in C and C++ flags. The
resulting reader files were byte-identical to each other and to the earlier
normalized relink, with SHA-256
`44672e42e3f68c889921615bb4072b50227ac415d24eb4e9ab5adc5a945aa976`.
Each build passed 31 libzim and 21 libkiwix test executables; two libzim
large-memory cases were skipped to stay within the 2 GiB limit. An identical
saved reader passed the current Linux offline GUI smoke test with the Chromium
sandbox requested.

The builds reused the same host environment, so matching bytes do not establish
independent toolchain provenance, completeness of corresponding source, or
reproducibility on another machine. The adapter still allows linker warnings,
including the static glibc compatibility warnings, to be nonfatal. Local build
and test logs are retained with the candidate-input evidence; Windows, macOS
and redistribution review remain separate work.

The source-only capture helper can obtain the pinned Git snapshots, dependency
archives and test caches into a new folder outside the repository:

```sh
python3 scripts/reader-rebuild/capture-inputs.py /path/to/new-candidates
```

It requires network access and Git, checks sizes and SHA-256 values against
`input-lock.json`, and refuses an existing output or insecure redirect. If a
source is unavailable or differs from the lock, it stops with a
`CAPTURE-INCOMPLETE` marker; it does not silently substitute unverified bytes.
A live capture of all 26 inputs and subsequent preparation succeeded on the
development host. This does not independently authenticate the lock or make the
captured files a complete corresponding-source package.

The source-only helper `scripts/reader-rebuild/prepare-inputs.py` prepares a
new experimental workspace from the captured candidate-input folder:

```sh
python3 scripts/reader-rebuild/prepare-inputs.py /path/to/candidates /path/to/new-workspace
```

It expects the pinned Git snapshots, dependency archives, test caches and a
`SHA256SUMS` file in the layout used by the local evidence. It checks the
supplied checksums and file sizes against the repository's `input-lock.json`,
extracts the snapshots with Python's data filter, and refuses an existing or
overlapping output. The lock records locally observed SHA-256 baselines; neither
it nor the supplied checksum file is publisher authentication. The repository
does not include these archives. A differently generated Git archive may be
rejected even when it contains equivalent source files. Preparation does not
isolate networking, install or provide a toolchain, run the build, or establish
corresponding-source or license compliance. The accompanying strict adapter
was used in the later experimental build described below.

On a Linux build host with the required compiler, libraries, Python build
environment, `unshare`, `ip`, `setpriv`, `ionice`, subordinate UID/GID mappings
and a user systemd manager, the prepared workspace can be built with:

```sh
scripts/reader-rebuild/run-isolated.sh /path/to/new-workspace /path/to/venv/bin/python
```

The workspace and Python directory paths must use only ASCII letters, digits,
slashes, dots, underscores and hyphens. The runner refuses an existing build or
log, creates a loopback-only network namespace, caps the build at one CPU and
2 GiB, and writes `build-run.log`. It skips the two large-memory libzim cases.
Once the build starts, a failure retains `REBUILD-INCOMPLETE` and its log;
prepare a new workspace to retry. The runner was exercised end to end on the
same host as the prior builds and produced the matching reader hash. It does
not make the source bundle complete or establish that another host has the
required dependencies.

An additional experimental adapter checks the Kiwix tools compilation log and
rejects warning lines other than the four observed static-glibc diagnostics.
It also rejects a missing log or skipped compile; a compiler or linker failure
remains fatal. A subsequent clean offline build passed with this adapter,
accepting eight known warning lines across its tools links and producing the
same `44672e42…` reader hash. A separate real relink with a deliberately
introduced duplicate-common warning was rejected by the policy. The policy
does not cover warning handling in other build phases and does not resolve the
glibc compatibility warnings. The earlier two builds used the original adapter.

This experiment demonstrates a possible source-build route, not that Kiwix's
prebuilt archive was made from these inputs, nor that the resulting reader is
ready for redistribution. System-provided libraries and all transitive build
inputs still require provenance and license review; other platforms and the
skipped tests remain unverified.

Before any redistribution, determine the actual source and build inputs for
each binary and its dependencies, collect the corresponding licenses and
notices, and review how to provide required source. Also review the Microsoft
runtime files used by Windows builds. The upstream [Kiwix Build](https://github.com/kiwix/kiwix-build)
project describes its build system, but the correct historical revision,
configuration and dependency inputs for these particular release archives have
not yet been established. The current binary records and upstream links do not
resolve those gaps.

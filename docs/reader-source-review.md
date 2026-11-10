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

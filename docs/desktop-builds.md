# Building desktop preview folders

The desktop window and its Go core can be assembled into a portable application
folder alongside Electron and a platform-matched Kiwix reader. This is a
**development build**, not a signed or released product. It does not contain
library archives or require them to be redistributed.

To build a local preview on Linux x86-64, Windows x86-64, or an Intel or Apple
Silicon Mac, install Python 3.11+, Go, Node.js and npm **on the build machine**,
then run from the repository root with an internet connection:

```sh
python3 scripts/build-desktop-preview.py --output /path/to/new-output-folder
```

Use `python` instead of `python3` where appropriate on Windows. The command
selects the host architecture, downloads temporary build tools and the checked
Kiwix reader, and assembles a folder without downloading any ZIM content. The
result runs without a separately installed Go, Node, or Kiwix. Windows uses the
matching C++ runtime DLLs from the build host; `--windows-runtime-dir` can select
a different source. Cross-target builds can use `--target`, but they still need
native testing. This is a development workflow, not authorization or clearance to
redistribute its binaries.

To assemble with a locally supplied reader and an extracted, platform-matched
Electron distribution instead of fetching them, use:

```sh
python3 scripts/build-desktop-preview.py --target linux-amd64 \
  --reader-dir /path/to/reader --electron-dir /path/to/electron/dist \
  --output /path/to/new-output-folder
```

The reader directory must contain `kiwix-serve` (or `kiwix-serve.exe` and its
required DLLs on Windows). With both inputs supplied, Node and npm are not
needed for assembly; Go and Python are still needed on the build machine. An
offline build also needs the Go modules already cached; set `GOPROXY=off` to
ensure the Go command does not fetch them. The packager checks executable
architectures and records local hashes. A supplied reader without provenance is
marked as such; supplying one does not complete source or license review.
This supplied-input path was exercised on Linux x86-64 with networking isolated,
cached Go modules, Electron 45.2.3 and the experimental source-built reader;
the assembled app passed the offline GUI smoke test. Other hosts and target
platforms still need native validation.

For a locally built Linux reader, a developer can record the claimed build
inputs before assembly:

```sh
python3 scripts/reader-rebuild/record-provenance.py --reader-dir /path/to/reader \
  --input-lock scripts/reader-rebuild/input-lock.json \
  --build-adapter scripts/reader-rebuild/offline-builder.py \
  --warning-policy scripts/reader-rebuild/link_warning_policy.py \
  --kiwix-build-commit 8062f23fb42454c9e6db319abe71d2add7263de7
```

This writes a new `reader-provenance.json` without replacing an existing one.
Assembly checks that its recorded reader hash matches the supplied bytes and
copies the recognized recipe hashes into `build-info.json`. It does not verify
that the claimed recipe produced that reader or that its source and licenses
are complete.

To supply the runtimes yourself for a Linux x86-64 development build, run:

```sh
python3 scripts/package-desktop.py --target linux-amd64 \
  --electron-dir /path/to/electron/dist \
  --reader-dir /path/to/kiwix-tools \
  --output /path/to/new-output-folder
```

The output folder must not already exist. It contains `offline-library`, the
native window, the Go core, and Kiwix. The app creates its default library in
its user-data folder and lets users select another library folder. It does not
include archives, Wi-Fi settings, or personal files.

Other supported assembly targets are `linux-arm64`, `windows-amd64`,
`darwin-arm64`, and `darwin-amd64`. Windows assembly also requires
`--windows-runtime-dir` with matching `msvcp140.dll`, `vcruntime140.dll`, and
`vcruntime140_1.dll`. Kiwix's Windows package also includes ICU DLLs; supply its
complete extracted folder so those are copied. macOS produces an unsigned app;
Electron 45 requires macOS 13 or later. Code signing, notarization and
Gatekeeper behavior must be addressed before a public release. Linux desktops must support Electron's platform libraries and
sandbox requirements. Architecture checks and cross-compilation alone do not
establish behavior on an actual operating system.

The bundler validates the architecture of input executables and Windows DLLs,
records local SHA-256 values for assembled binaries, and refuses an existing
output folder. For readers fetched by the one-command builder, `build-info.json`
also records the reported archive URL, publisher MD5, local SHA-256 baseline,
and extracted reader-file hashes; assembly checks the reader files against that
record. Supplied provenance is not an authenticity attestation. Manually supplied
readers without a record are marked as such; Windows runtime DLL origins and
licensing are not verified by this record. A separate [Windows runtime evidence record](windows-runtime-review.md) covers one local preview, not all builds. On a native build the one-command
builder also captures the bundled reader's own `--version` report, where
available. These self-reported versions do not prove which exact source or build
inputs produced a binary; cross-target builds are not executed for this report.
See [reader source review](reader-source-review.md) for the current evidence and
remaining gaps. Electron's distribution includes its own license notices. See
[third-party licensing](../desktop/THIRD-PARTY.md) for Kiwix source and Microsoft
runtime considerations before redistribution. The local build hashes are not
publisher signatures.

### Local Windows signing test

On Windows with Python 3.11+ and the Windows SDK, a developer can sign a
separate copy of a newly built Windows preview using a certificate already in
the Windows certificate store:

```powershell
python scripts/sign-windows-preview.py C:\path\to\preview C:\path\to\new-copy `
  --signtool C:\path\to\signtool.exe --thumbprint CERTIFICATE_THUMBPRINT
```

The output must not exist. The script verifies recorded binary hashes before
copying, signs and verifies the app executable and Go core, and records the new
hashes. The Kiwix reader and Microsoft runtime files are preserved. A failed
attempt leaves `SIGNING-INCOMPLETE` in the new folder; the original remains
unchanged. No timestamp service is contacted unless an HTTPS `--timestamp-url`
is explicitly supplied. Signature verification uses Windows trust policy.

CI tests the mechanics with a temporary self-signed certificate in its isolated
Windows runner, then removes the certificate. A test certificate does not
establish publisher trust or SmartScreen reputation. A real certificate,
timestamping, redistribution review, and end-user platform testing remain
separate release work.

A Windows x86-64 core test binary also passed four targeted tests under Wine
9.0 on Linux: bracketed library paths, combined search, shared reader-address
handling, and search pagination. Wine reported a missing 32-bit helper while
initializing its prefix. This limited compatibility check did not run the GUI
and does not replace native Windows testing.

A separate attempt to run the standalone Windows preview under Wine produced
a window tree containing an `Error` window; the rendered content and working
GUI were not verified. Wine also logged a missing 32-bit helper and a network
notification error, but their relationship to that window is unknown. A
virtual-display screenshot attempt stalled and was stopped under the resource
cap. This does not establish how the preview behaves on native Windows.

### Local macOS signing test

On macOS with Node 22.12+ and Xcode command-line tools, a developer can make a
separate ad-hoc signed copy of a macOS preview folder:

```sh
cd desktop/signing
npm ci --ignore-scripts --no-audit --no-fund
cd ../..
node desktop/signing/sign-copy.mjs /path/to/preview /path/to/new-signed-copy --adhoc
```

The output must not exist. The script checks the input's recorded reader and
core hashes, copies the folder, signs its nested code and app bundle, verifies
the signatures, and records post-signing hashes. An interrupted attempt leaves
`SIGNING-INCOMPLETE` in the new folder. The original is not modified. Ad-hoc
signatures are for local tests and do not provide Developer ID or notarization.
CI tests that path on both Mac architectures. The script also accepts an explicit
`--identity "Developer ID Application: …"` on a suitably configured Mac, but
that path has not yet been validated with a certificate. It does not submit to
Apple, create an installer or image, or publish binaries. A future notarization
and distribution workflow still needs credential handling, validation and the
third-party source and license review.

When enabled, `.github/workflows/desktop.yml` runs only when a release is
published, not on commits or pull requests. It runs the core tests, assembles a
temporary test folder, and exercises its window, reader, and offline search on
Linux, Windows, and macOS. The workflow is currently disabled and does not
upload release builds. CI downloads two small publisher-verified ZIMs solely
as temporary test fixtures, including a reader-address collision check; the
application folder does not include them. Kiwix
currently publishes MD5 checksums for its tool archives; the CI check compares
those and also checks locally recorded SHA-256 baselines. The SHA-256 values
are not publisher signatures, and MD5 is not strong authenticity verification.

### Linux offline check

On a Linux build machine with unprivileged user and network namespaces enabled,
`ip`, `unshare`, `setpriv`, `xvfb-run`, Node and the shell's Playwright dependency
available, the packaged app can also be exercised with networking isolated:

```sh
OFFLINE_LIBRARY_LIMIT_SCOPE=1 scripts/test-linux-offline.sh \
  /path/to/offline-library /path/to/fixture.zim /path/to/collision-fixture.zim
```

Use two verified, distinct test ZIMs, such as those fetched by
`scripts/ci-fetch-fixtures.py`. The check runs as the current non-root user in a
network namespace with loopback as its only interface; it does not change the
host's network settings. The optional scope setting uses the user systemd
manager to cap the test at one CPU and 2 GiB of memory. User namespaces need
working subordinate UID and GID mappings. The test uses temporary library data;
it does not modify the supplied app or fixture files. This Linux check does not
replace native Windows or macOS testing.

One Linux x86-64 development build was also tested offline against five real
archives: Wikipedia, Wiktionary, a locally repaired iFixit archive, and two
Stack Exchange archives. A `battery` title search returned matches from all
five, and one result from each returned an HTML page from the reader. The same
full-text search returned matches from the four indexed collections and
reported the fifth as lacking a full-text index. The test used hard links to
the original files in a temporary library and did not download or copy the
archives. This sample does not establish the quality or completeness of all
articles or collections.

On Linux the smoke harness explicitly requests Chromium's sandbox and checks
that Electron did not receive `--no-sandbox`, as well as checking the renderer's
sandbox preference. Earlier smoke runs used Playwright's default, which adds
`--no-sandbox` on Linux, and therefore did not validate that path. Developers
can set `OFFLINE_LIBRARY_SMOKE_NO_SANDBOX=1` where a sandbox cannot run, but
such a run does not validate sandbox operation.

### Live publisher check

A local Linux x86-64 standalone development build was also exercised against
the live Kiwix catalog and download service using the small Vue Docs collection.
The GUI searched the catalog, added the collection to its queue, downloaded it,
verified it, opened it in the reader and searched its titles. A separate run
seeded a 16 KiB matching partial and successfully resumed and verified the same
975,797-byte archive. Both completed files matched the previously recorded
SHA-256 baseline `33d5231e492d05a47cc844afcb13540d61ff8acc938329e1118ea264fd8de70b`.
This checks one publisher collection and is not proof that all mirrors or
collections support resumption. The sandboxed Playwright run required a user
namespace on that host; normal interactive desktop operation was not verified
by this test. These online checks complement the isolated offline test above.

On the same host, a direct launch of the standalone preview under a virtual
display failed because Electron could not use its sandbox helper. Launching
inside a user namespace with subordinate ID mappings and dropping back to the
regular user produced the application window without `--no-sandbox`. This
workaround depends on host tools and configuration and has not been validated
as a general portable launcher; it does not establish interactive desktop
compatibility. A service-style test on this host could not set its groups.
Earlier preview folders included `start-with-user-namespace.sh` as a manual
workaround requiring `unshare`, `setpriv`, and subordinate mappings. Its group
changes can fail in restricted desktop sessions. The automatic native Linux
entry point described below supersedes that wrapper.


### Automatic Linux startup

Linux builds use a native `offline-library` entry point with the unchanged
Electron runtime in `offline-library-bin`. The earlier namespace wrapper was
not sufficient under Ubuntu's ordinary desktop AppArmor profile: namespace
probes could succeed while Chromium itself was denied sandbox namespaces.

The entry point now keeps native Chromium startup. When a compatible system
SUID helper is already installed, it verifies root ownership, setuid mode,
non-writable parent directories and a matching sandbox API version before
linking `chrome-sandbox` to it. The original bundled bytes remain in
`chrome-sandbox.bundled`. No chmod, root installation, global policy change or
sandbox-disabling flag is applied. Moving the folder preserves its bundled
helper; the launcher can select another compatible system helper on a new host.
A host that blocks unprivileged sandbox namespaces and has no compatible
installed helper requires a supported installation; it fails closed.

The normal launcher must be tested offscreen under the ordinary `unconfined`
AppArmor desktop context, rather than only the agent's `chatgpt` profile, which
has different namespace permissions. The old script name delegates to the
normal entry point. Keep the entire folder together when moving it.

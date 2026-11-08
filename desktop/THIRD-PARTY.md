# Third-party components

The desktop window includes Electron and Chromium. Their license notices are
included in the supplied Electron distribution as `LICENSE` and
`LICENSES.chromium.html`. The Go core uses `golang.org/x/sys` under its BSD
license; see https://pkg.go.dev/golang.org/x/sys for source and license.

The local iFixit repairer is original Go code under this project's MIT license.
Its compiled dependencies are `github.com/klauspost/compress` v1.17.11 (Zstandard),
`github.com/ulikunitz/xz` v0.5.12 (XZ), and `golang.org/x/net` v0.33.0 (HTML
tokenizer), all under BSD-3-Clause. Their notices are included in
`THIRD-PARTY-LICENSES`. No third-party collection content is bundled with it.

The reader is Kiwix tools, licensed under GPL-3.0-or-later. Source and license:
https://github.com/kiwix/kiwix-tools. Kiwix uses additional components including
libkiwix and libzim. Before redistributing a bundled binary, prepare the complete
corresponding source and notices required by its licenses, including the exact
versions and build inputs. A source link alone is not a determination that all
redistribution obligations have been satisfied.

Windows builds also use Microsoft Visual C++ runtime files. Supply those files
from a properly licensed Microsoft redistributable and retain the applicable
license and notices before distribution. Locally generated SHA-256 values in
`build-info.json` document the assembled bytes; they are not publisher checksums.

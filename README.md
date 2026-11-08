# Offline Library Kit

Build a searchable library that works without an internet connection. Collect
reference archives, read them locally, and optionally serve them from a Raspberry Pi.

## Desktop app

The **desktop preview** downloads and verifies ZIM collections, then searches
the contents with a hybrid search engine: existing Kiwix indexes plus the
[included supplementary indexes](search-indexes/README.md). iFixit link repair
and preparation of a compatible search index run automatically after download.
Development builds bundle a reader in a native app window; see
[desktop builds](docs/desktop-builds.md). Ready-to-run
releases are still in development. Raspberry Pi card creation is planned later.

To try the preview with Go and a compatible `kiwix-serve` already available:

```sh
cd desktop
go build -o offline-library .
./offline-library --library /path/to/library --reader /path/to/kiwix-serve --search-indexes ../search-indexes
```

Use **Open library** to start the reader and open it in your browser. The app
stores downloaded archives in the library's `content/zim` folder. The reader
and control panel are accessible only from the local computer.

## Build a library

The toolkit also includes scripts to stage and verify downloads, copy archives
to an existing drive, audit and repair iFixit links, and run a library on a
Raspberry Pi. The repo includes about 683 MB of derived search data in ordinary Git parts;
it does not include ZIM archives, readers, or a finished disk image.

See the **[full guide](docs/guide.md)** for setup, copying, Pi hosting, iFixit
coverage, and validation. See [AGENTS.md](AGENTS.md) for project safeguards.

Reference material may be incomplete or outdated. Check publisher licenses,
verify downloads, and test the finished library on the devices you plan to use.

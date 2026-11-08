# Offline Library Kit guide

Run the commands in this guide from the repository root unless noted otherwise.

Build a personal, searchable library that works without internet access. Use fast local storage to stage downloads, verify each file, and copy archives one at a time to a removable drive. An optional Raspberry Pi setup serves the library to a touchscreen and local network.

This repository contains software, templates, and prebuilt derived search indexes. It does not contain reference archives, installers, scraped content, personal files, device-specific manuals, Wi-Fi credentials, or finished images. Choose content from its publisher, respect its terms and license, and check its date and applicability.

## What is included

- A manifest-driven downloader with concurrent SSD staging and SHA-256 verification.
- A serialized, verification-first copy command for mounted archive drives.
- A small touchscreen-oriented landing page and optional Pi desktop autostart scripts.
- An optional QR generator for a Wi-Fi connection and a local library URL.
- Optional iFixit coverage auditing and creation of a separate copy with repaired internal links and coverage notices.
- [AGENTS.md](../AGENTS.md), a checklist for adapting this project with a coding assistant.

The Python downloader and drive-copy code currently targets Linux: it uses `curl`, filesystem mount checks and advisory `flock` locks. The Pi scripts assume a graphical Linux desktop, Python 3, Chromium and `kiwix-serve`. Windows and macOS readers can be acquired separately from their publishers; the downloader and Pi setup are not tested on those systems.

## Plan and download

1. Install Python 3.10+ and curl. Find the collections you want, such as ZIM archives from [Kiwix](https://library.kiwix.org/), and record the publisher's exact file URL, byte size and independently published SHA-256. Record provenance and licenses separately. A hash you calculate yourself is useful for later comparison but does not establish publisher authenticity.
2. Copy `examples/manifest.json` to `manifest.json`, then fill `items` with entries in this form. The placeholders are deliberately not working downloads:

   ```json
   {
     "items": [{
       "name": "collection.zim",
       "url": "https://publisher.example/collection.zim",
       "bytes": 123456789,
       "sha256": "replace-with-the-64-character-published-sha256"
     }]
   }
   ```

3. Put staging on a filesystem with enough space for the entire batch plus the reserve:

   ```sh
   python3 archive.py download manifest.json --stage /path/to/fast-staging --jobs 2 --reserve-gib 10
   ```

Downloads resume with curl when the server supports byte ranges. Files remain `.part` until both size and SHA-256 match. A failed or conflicting file is retained for inspection; nothing is silently overwritten. Only list URLs and hashes you have independently verified. Keep private manifests and paths out of source control.

## Copy to an archive drive

Mount the destination, verify it is the correct drive, and initialize its mount root once:

```sh
python3 archive.py init-target /path/to/mounted-drive
```

Save the printed target ID. Then copy from staging:

```sh
python3 archive.py publish manifest.json --stage /path/to/fast-staging \
  --target /path/to/mounted-drive --target-id YOUR-TARGET-ID --reserve-gib 10
```

The command checks the mount and target marker, checks each staged source, writes a new `.part`, flushes it, verifies the destination and renames it. Files are written sequentially. A lock coordinates publish processes **using the same staging directory**; it does not coordinate unrelated copy programs or other staging directories. Do not run simultaneous writers to a drive that performs poorly under mixed writes. A target marker is an accidental-destination check, not proof of device identity. Check mount and device identity yourself, especially after reconnecting hardware. Existing partials and mismatches require manual inspection. Keep an independent backup and perform actual restore tests.

## Raspberry Pi and local sharing

Install Raspberry Pi OS and complete first boot, account setup and filesystem expansion on the Pi. Obtain a compatible `kiwix-serve` and Chromium from their official sources and test them on the device. Make a library folder, copy `examples/index.html` to its root as `index.html`, and put verified ZIMs under `content/zim/`. Add links to local manuals and maps to that page. The default directory is `~/Offline-Library`.

Run `pi/start-library.sh` to serve the landing page on port 8765 and Kiwix on port 8766. The services bind to all IPv4 interfaces so connected devices can read the files: **serve only material you intend to share**. On the Pi, `pi/open-library.sh` waits for the page and opens it in Chromium. Once manually tested, `pi/install-autostart.sh /path/to/offline-library-kit` installs user-level desktop launchers; use a path without spaces. It also configures a plain black desktop background using `pcmanfm`. Remove `~/.config/autostart/offline-library-background.desktop` if you prefer your own background. It does not configure or flash an operating system, create accounts, configure a hotspot or change system networking.

To share without internet, configure a password-protected hotspot using the Pi's network manager and confirm the IP address it assigns. An optional QR generator creates two PNGs using the separately installed Python `qrcode` package with Pillow support:

```sh
python3 pi/generate-qr.py --ssid 'Your Network' --password-file /path/outside/shared/library/password.txt \
  --url 'http://YOUR-PI-ADDRESS:8765/' --output-dir /path/to/output
```

The Wi-Fi QR encodes the password. Only place or display it where you intend to share access; do not commit it or the password. To show the images on the landing page, copy them into the shared library and add image tags, understanding anyone who can open that page can retrieve the Wi-Fi code. Test phone scanning, touchscreen use, boot, hotspot startup and offline operation on your own hardware. A printable PDF can be made from the generated QR images; this kit does not generate one automatically.

For larger libraries, build a portable catalog once after adding or removing
archives. This avoids rescanning every archive's metadata on each server start:

```sh
python3 index_library.py /path/to/library/content/zim \
  --output /path/to/library/library.xml --manager /path/to/kiwix-manage
```

The Pi launcher uses `library.xml` when present. Rebuild the catalog whenever the
selected archives change. The default stored paths are `content/zim/FILENAME`,
relative to the catalog. Metadata may also be read from fast staging storage if
it contains the same selected filenames (symlinks are supported), while the
catalog is saved in the library root. The index is metadata, not a copy of the
archives, and does not verify their contents.

## Validate and maintain

### Inspect iFixit coverage

An intact ZIM can contain empty device pages, missing links and material omitted
by its original scraper. Install the optional dependency in a virtual environment:

```sh
python3 -m venv .venv
.venv/bin/pip install -r requirements-audit.txt
.venv/bin/python audit_ifixit.py /path/to/ifixit.zim --output /path/to/new-audit-directory
```

The report distinguishes guide links, descriptions, media-only pages, navigation,
empty devices and guides without step sections. It checks internal links and
resources. These structural checks do not establish repair completeness or
accuracy. Auditing never downloads content or changes the input archive.

To make a separate local adaptation:

```sh
.venv/bin/python repair_ifixit.py /path/to/ifixit.zim \
  --audit /path/to/new-audit-directory --output /path/to/ifixit-local-repaired.zim
```

This repairs unambiguous links using existing numeric IDs, case/space differences
and incorrectly encoded section fragments. It adds coverage notices to empty
device pages and guides without steps, plus a browse index of guides with step
sections. It preserves the input and refuses existing outputs. It does not recover
omitted documents, forum discussions or videos. Local adaptations retain iFixit
attribution and CC BY-NC-SA 3.0 licensing. Test the result in your intended reader;
keep generated reports and archives outside this repository.

### Routine checks

Run `python3 -m unittest discover -s tests` and `python3 -m py_compile archive.py pi/generate-qr.py`. Exercise searches and representative articles with networking disabled, verify PDFs and maps on each intended device, and test a restore from each drive. Check reference dates: downloaded information can be incomplete or outdated. A checksum establishes agreement with the expected bytes, not the accuracy, safety or completeness of the content.

## Desktop control-panel preview

The desktop preview guides you through choosing a location, selecting collections,
reviewing downloads, and reading. Choose this computer or pick a mounted drive.
The app uses an Offline Library folder in your home directory or on that drive;
a custom folder can be chosen in Advanced settings. No drive is formatted. Local builds suggest a 32 GiB
reserve, while direct external builds suggest 2 GiB. Both can be changed.

For an external destination, staging on this computer or another fast drive is
the default. It keeps unfinished downloads and repair working files off the
destination, where they could otherwise consume space needed for the finished
library. Direct downloads remain available. Copy the finished ZIM collections to a
separately selected destination. Copying verifies each file, leaves the staging
originals, skips matching existing files, and never replaces conflicting files.
Set a destination reserve separately and reselect the destination after restarting
the app. Open the destination library by selecting its folder after copying. Staging and destination must use different filesystems. On Linux the app also
checks discoverable backing disks, including partitions and common mapped drives.
Physical-disk detection can be unavailable. In that case the app asks you to
confirm that the selected locations are on different physical drives. The
app shows separate build-space and finished-library estimates and compares them
with free space and reserves on both drives. Direct builds need room for the
originals, repaired copies, and temporary indexing files. When only the finished
library fits the destination, the app recommends separate staging storage if it
has enough build space after its reserve. iFixit index preparation allows 4 GiB;
see the [fresh space measurement](ifixit-space-measurement.md) for observed sizes
and limits. [wikiHow measurement](wikihow-space-measurement.md) records its
4.5 GiB peak and 64 MiB finished-index allowances and the shared sizing model.
[Gutenberg measurements](gutenberg-space-measurement.md) cover all 12 curated
subjects, with separate peak and finished allowances for each subject. Finished index allowances accumulate
across selected collections while temporary indexing workspace is reused.
Existing files and unknown sizes can
make estimates uncertain; every write still checks free space. An optional quick benchmark writes 32 MiB sequentially and runs three mixed-access
files. The extended test writes 16 GiB sequentially plus about 1.5 GiB across
three mixed-access files. Both respect the chosen reserve and can be canceled;
continuing to another step also stops the test and removes its temporary files.
Neither test can identify SMR or reliably predict sustained performance.
Staging copies recheck destination free space before each write. Recovery tests
use fixture files and simulated disconnects, cancel during copying and readback
verification, terminate a copy subprocess abruptly, and resume a download after
terminating its subprocess. Completed destination files and staged originals
are retained; orphan copy partials from abrupt termination are preserved and
ignored on restart. These tests do not simulate filesystem corruption or prove
physical power-loss durability.

The desktop preview provides a local, browser-based control panel for
starting a Kiwix reader and downloading up to four ZIMs at a time by default. The shared collection
selection is shown first, with a text-focused 32 GB option, a 256 GB selection
and a 512 GB option. By default the app selects the largest preset that fits the
currently available space after reserves, including local repair overhead and
both drives when staging. Unknown space or sizes are not treated as a fit. If
no preset fits, choose individual collections or another drive. Manual presets
and checkbox changes are kept when the location changes within the app. The compact choice leaves wikiHow and iFixit
unselected because the available editions include images; they remain optional;
change the selections and add them to the download list. The presets are starting
points, not guarantees of an exact filled capacity, particularly while sources
such as optional collections have unknown sizes.
Catalog search and publisher-specified downloads remain available. The shared
list uses dated catalog entries and estimates, so individual entries can become
unavailable. The wikiHow selection downloads the archived 2023 file from Kiwix. Its SHA-256
is a local baseline, not a publisher checksum, and known source images are missing.
The iFixit collection is downloaded from the publisher catalog. After verification,
the app automatically shows **Repairing links…** while it creates and verifies a
separate local copy. The original stays on the drive; the reader shows the repaired
copy once it is ready. Space estimates allow for both copies and repair overhead.
Pausing keeps the original and unfinished output; continuing verifies the original
and restarts the repair without downloading it again. Re-adding an already downloaded
publisher iFixit collection also performs this repair. Existing outputs are never
overwritten. The repair corrects unambiguous links and labels empty pages; it cannot
restore missing instructions. It retains the original search index, which does not
include the added notices, and does not create an extra browse index. The Go repairer and search indexer
need no Python or separate runtime installation. After repair, the app builds an
index bound to the new archive version; it never reuses an incompatible old index. No collection is redistributed.
Add catalog collections to the
download list. **Simultaneous downloads** defaults to 4 and can be set from 1 to 8
while paused; the limit is saved with that library. Use 1 for slow archive drives.
**Total download limit** is a shared cap in MiB/s (binary megabytes per second).
It defaults to 0 for unlimited and can be changed while running, from 0.001 to
1024 MiB/s. It is saved per library and covers manual and queued archive body
transfers together. Small buffered bursts can occur; this is application pacing,
not a system network limit. Metadata requests, verification, repair, indexing and
local staging copies are not throttled. Deliberate bandwidth waits do not trigger
network stall detection, and Pause interrupts those waits immediately.
Each active download shows its received bytes, recent effective transfer speed,
and approximate transfer time remaining. Speed uses a rolling 10-second window
with a one-second warmup, includes storage delays, and excludes bytes already in
a resumed partial. Reconnecting starts a fresh measurement. Speeds decay during
stalls; without a useful rate the ETA is unavailable. The list also shows
combined speed and an estimate for its current transfers, based on the slowest
individual ETA. These estimates exclude queued items, partial checking, final
verification, repair, and search preparation; preparation is shown separately.
They are not estimates of when the entire library will be ready.
Repairs and search-index construction
remain serialized, and workers can wait for space rather than overcommit it.
The list-wide **Pause** stops all active workers and retains matching partials.
Each active item also has its own **Pause** control; other items continue and the
freed slot can run the next unpaused item. An individually paused item stays
paused across restarts and is skipped by the list-wide Start button. **Resume**
continues that item; when the list is stopped, an item’s **Start** or **Resume**
starts only that item. Pausing during repair or indexing keeps recoverable output,
but that preparation stage may need to restart. Temporary connection
failures, HTTP 408/429, and server errors automatically retry up to three times
with backoff (and a bounded Retry-After delay). **Reconnect after no data for (seconds)** defaults to
60 seconds and is adjustable from 15 to 600 seconds alongside simultaneous
downloads while stopped. It is saved per library. Waiting too long for response
headers or the next body bytes triggers the same reconnect-and-retry path.
Slow transfers that continue delivering bytes are allowed; hashing and disk
writes do not count toward network idle time. Matching partials resume when the
publisher supports byte ranges. Checksum mismatches, changed publisher details,
unsupported resume responses, filesystem errors, and file conflicts pause for
review. After three automatic network retries are exhausted, only that item
pauses with **Retry** and a failure notice. Other workers continue and its slot
can run the next unpaused item. Matching partials are retained. The failed item
stays excluded from list-wide Start across restarts until you explicitly choose
its **Retry** control, which checks current publisher details before resuming.
This applies to transient catalog-metadata failures as well as archive downloads.
Integrity, storage, reserve, and download-list save failures still pause the
whole list for review. Restarting the application always leaves the saved list paused.
The list shows estimated full archive sizes from the catalog, including any
items with unknown sizes. **Space now** separates filesystem free space, the
free-space reserve, committed active download bytes, and conservative preparation
allowances. Available space subtracts those active commitments after the reserve;
preparation allowances can exceed actual temporary use and are released when
workers finish. Remaining download bytes use exact active reservations and
estimated queued sizes minus recognizable resumable partials. The estimate
includes individually paused items; existing complete archives and changed
publisher editions can make it too high or too low. Unknown sizes are shown as
a lower bound, never an exact total. Pending collections are included in the
remaining estimate, not in active commitments. Estimates do not reserve disk
space against other applications; runtime write checks still protect the reserve.

**Recent downloads** retains the newest 50 completed or failed outcomes with
timestamps, automatic retry counts across metadata and archive requests, and the
last error HTTP status when available. Expand an entry for its failure reason.
Completed means archive verification and required repair/index preparation have
finished. Cancellation retains unfinished queue items and does not create a
completed history entry. History is saved atomically with list changes in that
library and restored after restart. It omits download URLs, including query
tokens and URL credentials. Old process interruptions may leave no outcome, but
the persisted unfinished item remains available for recovery.
Lists created by earlier versions remain readable, with their sizes unknown.
Older previews may be unable to read a list after sizes have been saved; use
the newer app to manage it.
Older previews may reject a list with saved concurrency, stall-timeout, or
individual-pause, network-failure, bandwidth or history settings; use the current app to manage it. The list is saved in the selected library and remains paused after restart;
start it explicitly to continue. Recognized partial downloads remain available
for recovery. Switching libraries loads the list belonging to the new folder.
Set **Space to leave free** for each library before downloading; the underlying
minimum is 2 GiB and it can be raised to leave room for an operating system or other files.
The app checks the reserve before each download, but other processes can use
space afterward. The free-space display refers to the collection destination,
including when it is on a different filesystem. Before that folder exists, it
uses the nearest existing parent. The reserve is saved with the download list. Older previews
do not honor this setting and may reject the updated list, so use a current app
when the reserve matters.
If the app stops after a file is installed but before its list is saved, the
item may remain listed. On the next start, the app checks an existing archive
against the current publisher size and SHA-256 and marks it complete only if it
matches. A different or unrecognized file is retained and the list pauses. Topic buttons start catalog searches;
they do not guarantee that a particular collection is currently available.
It opens the panel automatically, opens the reader on request, binds both to
localhost, and checks download size and SHA-256 before adding the file. Search
downloaded collections by title or name, or search full text across collections
that have an index. Use **Load collections** to choose a specific downloaded
collection before the first search. If two archives have the same Kiwix reader
address, the app reports both as unavailable for search: Kiwix may serve only
one archive at that address, so search links would be ambiguous. Other
collections can still be searched. Kiwix derives addresses from filenames and
normalizes characters, so different filenames can collide.
Some archives, including Gutenberg examples, have no full-text
index. Search results are paged in groups of up to 15 per collection; unavailable
collections are reported. A separate Go indexer has an initial search implementation; see the
[provisional integration notes](search-indexer-integration.md). The
panel can stop the reader and exit the application. A complete catalog with
matching paths is used when present; otherwise the reader scans the archives.

The native desktop development builds bundle the reader and open this panel in an
application window; see [desktop builds](desktop-builds.md) for packaging and
platform limitations. Raspberry Pi card creation is planned for later. The app
can retry a failed or canceled download, including a failed catalog lookup,
resuming a matching partial where the publisher supports byte ranges. Catalog
retries check the publisher details again. It can also discard a recognized incomplete
download. Saved incomplete downloads are listed after a restart. Catalog downloads
created by current versions can be resumed from that list after publisher details
are checked again; older and manual downloads can be resumed by selecting the
same collection or re-entering its original details. The app can also discard
a recognized partial from the list, including after a restart. Unrecognized or
mismatched partials are retained for inspection.
Completed files are re-read before installation. Installation requires a
filesystem that supports a safe no-overwrite rename or hard link.
When the library contains `index.html`, the app serves that homepage and local
manuals on loopback port 8765, with Kiwix on port 8766 for existing Pi-style
links. If either port is busy, it opens Kiwix on another local port and reports
that the homepage is unavailable. Only use a library folder whose contents are
appropriate to serve locally.

For development, with Go installed, build the application and put the appropriate
Kiwix binary next to it, or specify the reader explicitly:

```sh
cd desktop
go build -o offline-library .
./offline-library --library /path/to/library --reader /path/to/kiwix-serve
```

The library folder stores downloaded archives under `content/zim`. The desktop
window remembers the selected folder's filesystem identity and asks you to
reselect it if it is missing or has changed at startup; older saved selections
need a one-time confirmation. While running, the core checks the folder identity
before downloads and cleanup. These checks reduce accidental writes after a
drive change; they do not replace checking the actual drive or prevent every
possible unplug or replacement race. Create a folder before passing it to the
standalone Go control panel with `--library`. The app
checks available space before downloading and keeps a 2 GiB reserve; it will still
finish verifying a complete partial when less space is free. If `--library`
is omitted, the executable's directory is used. On Windows the executable is
`offline-library.exe`; the standalone Go control panel requires a compatible Kiwix binary and its
runtime dependencies. The
application shows its local panel address in its terminal if the browser cannot
be opened. Stop it with **Exit app** or Ctrl-C. The Wi-Fi sharing and Pi workflows
are separate from this localhost-only preview.


### Hybrid search

The desktop app defaults to Search everything. It reuses native Kiwix full-text
search, and queries the included compact index for collections without it.
Titles, authors and subjects use the included basic catalog when its exact
archive version is present. Missing metadata is left unknown. Older title
suggestions and native full-text controls remain available as fallbacks.

The included index set uses 682,922,150 binary bytes (about 682.9 MB), plus small
manifests and attribution notices. It is split into 64 MiB ordinary Git files;
Git LFS is not required. A build copies the index set into the library; staged
copies include finished indexes and omit partial extraction/sort work. Capacity
suggestions allow 700 MB for these assets, plus conservative local-index workspace
for custom collections. Each write still protects the free-space reserve. The Go
core defaults to two execution threads and a 768 MiB soft Go-memory limit;
GOMAXPROCS and GOMEMLIMIT can override these. The bundled reader uses two worker
threads. These are process defaults, not whole-system hard resource limits.

Only exact UUID, byte-size and stored-checksum matches use a prebuilt pack. Those
identity checks supplement full archive download verification. The older iFixit
adaptation in the included pack is intentionally incompatible with a newly
repaired archive. The app builds a new custom-only index from that local file
instead of changing its binding. Matching publisher versions skip extraction.
Updated Gutenberg, wikiHow and map navigation collections can also be indexed
locally. Unsupported archives leave the original and failed work intact.

Custom search matches all supplied Unicode words on a page, not phrases or
nearby passages. Native stemming/count estimates can differ. Results are
interleaved across collections, with working results retained if a backend fails.
Maps coverage is HTML/navigation rather than place/road/tile search. Pi and
microSD performance have not been established by desktop benchmark timings.
See [index provenance and format](../search-indexes/README.md).


## Reading and exploring

An existing, available library opens on the reading screen. New and unavailable
libraries still open Location. Search is at the top; collection cards let you
open a collection or focus a search on it. Reader processes start in the
background when needed. Your library and Manage downloads shortcuts keep reading
and management within reach. Opened this session lists up to six pages opened
from this app for the current library; it is cleared when the app closes and
does not track navigation inside the reader.

Collections offers Fix things, Grow food, Cook & make, Learn & explore, and
Stories & books starter packs. Each shows included collections, limitations,
and build/finished size estimates. Adding a pack preserves existing selections,
selects whole archives and starts no downloads. Review the selection and storage
estimate before proceeding. Packs draw from the existing dated catalog entries.
Community answers and older books are not verified comprehensive guidance.

Content type and Collection subject narrow both collection browsing and search.
These describe collections, not every page inside them. Filters apply before
searching and pagination; changing a filter clears old results and requires a
new search. Result cards show collection attribution, content type, author and
subject metadata when available, and safely highlighted previews. Where a
preview or full-text index is unavailable, the interface says so.


### Reading and management navigation

Empty libraries show setup and adding content, with reading actions hidden.
Available libraries open directly to reading, with setup steps tucked away.
Manage library is the secondary route to starter packs, the editable download
list (including removing pending items), and location/storage controls. Read
your library returns from management. Reader controls also live in management.
A first collection becoming available exposes reading immediately; if a build
is still running, progress stays visible until it finishes. Missing drives or
an empty selected library return to setup and hide reading again. Error and
paused-download screens remain visible for review.

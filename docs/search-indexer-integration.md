# Future search indexer integration

A separate Go search indexer has an initial search implementation; full-text
search is not implemented there yet. Its API, supported formats and availability
to this project have not yet been verified. This is a planning note, not a requirement
that it adopt a particular protocol or a claim that it can index ZIM files.
The current Kiwix search remains available while that work develops.

## What the desktop app needs

- Search downloaded material without a network connection, including multiple
  words, with result titles, collection names and links that open the source
  material. Report which collections were searched, skipped or unavailable.
- Preserve Kiwix title and full-text search where suitable, including for users
  who have not built or downloaded an additional index. A missing, stale, broken
  or incompatible optional index must not prevent the library from opening.
- Keep source archives unchanged. Store generated indexes separately in the
  selected library, identify the source and index versions they cover, and
  provide a way to rebuild after content changes. Never delete an unrecognized
  index or source file automatically.
- Support bounded, interruptible index building with visible progress, storage
  requirements and failure reporting. Avoid heavy CPU, memory and disk use by
  default; the library may be on removable media or a small computer.
- Treat archive metadata, search terms and result content as untrusted; prevent
  paths or links from escaping the library and avoid network access for searches.

## Integration decisions to make together

When the indexer has a stable design, establish its supported inputs (including
whether it can extract ZIM articles and manuals), query semantics and ranking,
index size and build cost, resource controls, index compatibility/versioning,
update and recovery behavior, and supported platforms. Then choose an interface
that fits the actual implementation and package it with the app if appropriate.
Test representative collections with and without existing Kiwix indexes, measure
resource use on the intended hardware, and verify offline results and links. Do
not promise full-text coverage or performance until those tests exist.

# photocull

A fast, safe command-line tool for finding and removing duplicate photos — the exact copies **and** the ones that only *look* the same.

Written in Go. Ships as a single static binary with no runtime dependencies.

---

## Why this exists

I had an external drive holding years of photos, pulled together from several phones and a tangle of half-finished backups. The same picture existed three or four times over: once from the phone, once from a backup that resized it, once re-compressed by a messaging app, once as an iPhone HEIC next to its JPEG twin. Tens of gigabytes of the drive were duplicates, and no folder-by-folder cleanup was ever going to untangle it by hand.

I could have reached for an existing dedup tool. Instead I built the one I wanted — because "clean up my photo drive" turns out to be a genuinely interesting systems problem: read thousands of files fast, recognise the same photo across resizes and formats, and never, ever lose one to a bug.

`photocull` is the result, and it did the job on the real drive: **~1,900 photos, ~9 GB**, deduplicated down without a single permanent deletion.

## What it does

- **Exact duplicates** — byte-for-byte identical files, found by SHA-256. Zero false positives; safe to remove on sight.
- **Similar photos** — the same image after resizing, re-compression or format conversion, found by a perceptual hash and grouped by visual distance. These are a judgement call, so photocull makes them easy to review rather than deleting them for you.
- **HEIC/HEIF** — iPhone photos are decoded and matched alongside JPEG/PNG/GIF, so a HEIC and its JPEG export land in the same group. No system libraries required (libheif runs as WebAssembly).
- **Add to a library** — point it at a library folder and a source folder (say, a phone dump) and it copies over only the photos the library does not already have, exact or similar. The library is never modified except by adding; the source is never touched.
- **Safe by construction** — nothing is ever deleted permanently. Duplicates go to the **system recycle bin**, and only after you confirm. The default is always a dry run.
- **Visual review** — a native desktop window (or the browser) shows the photos as thumbnails, full-size on click, so you decide by eye which to keep or bring in.

## Install

```sh
# From source (Go 1.26+)
go install ./cmd/photocull

# Or grab a prebuilt binary from the Releases page — no dependencies to install.
```

## Usage

### Just run it — the app

Double-click `photocull.exe` (or run it with no arguments). It opens a native window (WebView2 on Windows; falls back to the browser if that runtime is missing) where you **pick a mode and a folder**, then review and act — no command line needed. A native folder picker is one click away.

```sh
photocull              # opens the app window
photocull --browser    # force the browser instead of a native window
```

The three commands below are the same duplicate-finding functionality from the terminal.

### See what's there — `scan`

`scan` only reads. It moves nothing.

```sh
photocull scan ~/Photos                       # exact duplicates
photocull scan ~/Photos --similar             # also resized / re-compressed copies
photocull scan ~/Photos --similar --json      # machine-readable report
```

```
Group 1  [similar]  3 files, 4.8 MB reclaimable
  KEEP    2019/summer/IMG_4021.jpg       4.9 MB  4032x3024  2019-07-14
  delete  backups/phone/IMG_4021.jpg     1.2 MB  1600x1200  2021-03-02
  delete  whatsapp/IMG-4021-copy.jpg     0.4 MB  1024x768   2021-08-19

Found 128 duplicate groups: 96 exact, 32 similar
241 files could be removed, freeing 9.3 GB
```

### Remove them — `clean`

Dry run by default; add `--confirm` to actually move duplicates to the recycle bin.

```sh
photocull clean ~/Photos                       # dry run: shows the plan, changes nothing
photocull clean ~/Photos --confirm             # asks before moving anything
photocull clean ~/Photos --confirm --yes       # no prompt (for scripts)
photocull clean ~/Photos --similar --threshold 6
```

### Review by eye — `serve`

For "similar" matches, a thumbnail beats a file path. `serve` opens a local page (localhost only) where you tick the copies to remove and send them to the recycle bin.

```sh
photocull serve ~/Photos --similar
# → Review at http://127.0.0.1:8080
```

### Choosing which copy to keep

Within a group, photocull suggests keeping the highest-resolution copy, breaking ties by larger file, then oldest timestamp, then shallowest path. It is only a suggestion — every duplicate is pre-selected but you can change any of them. Override the rule with `--strategy` (`default`, `resolution`, `oldest`, `largest`).

## How it works

**Concurrent scan.** The scanner is a three-stage pipeline connected by channels: one goroutine walks the tree, a pool of workers (one per CPU core) reads and fingerprints files, and the results are funnelled back and aggregated. Each file is read from disk **exactly once** — an `io.TeeReader` feeds the SHA-256 hasher and the image decoder from the same byte stream — because on a drive with thousands of photos the disk, not the CPU, is the bottleneck. A single corrupt file is reported and skipped, never fatal.

**Two kinds of match.** Exact duplicates come from a content hash. Similar photos come from a 64-bit perceptual hash compared by Hamming distance, with a configurable threshold. Groups are formed with **union-find**, because visual similarity is not transitive: A can resemble B and B resemble C without A resembling C, yet all three are the same photo and belong in one decision.

**Safe deletion.** Removal always goes through the OS recycle bin (Shell32 on Windows, the FreeDesktop trash spec on Linux). There is no code path that deletes without an explicit human action in that same run.

## Project layout

```
cmd/photocull        entry point
internal/scanner     concurrent walk + fingerprinting
internal/hashing     SHA-256 and perceptual hashing
internal/imageutil   format decoding (incl. HEIC) and thumbnails
internal/dedupe      grouping (union-find) and keep heuristics
internal/pipeline    scan + group + stats, shared by the CLI and the launcher
internal/trash       recycle-bin wrapper (behind an interface, for testing)
internal/osdialog    native folder picker (Windows), for the launcher's Browse button
internal/report      stats and human/JSON output
internal/webui       launcher + review server (embedded static assets)
internal/cli         cobra commands: launcher, scan, clean, serve
```

## Testing

```sh
go test ./... -race -cover
```

The test suite runs entirely offline against small synthetic fixtures and **never touches the real recycle bin** — deletion is tested through an injected fake. Coverage of the core logic (grouping, hashing, scanning, the web API) sits above 87%, including the security guard that stops the review server from serving any file outside the scanned set. CI additionally cross-compiles for Windows, Linux and macOS with `CGO_ENABLED=0`, which is what keeps the single-binary promise honest.

## Notes

- Module path is `photocull`; rename to `github.com/<you>/photocull` when publishing.
- The perceptual `--threshold` (0–64, default 8) trades recall for precision. Lower is stricter. Review similar groups before deleting.

# photocull

A fast, safe command-line tool for finding and removing duplicate **photos and documents** — the exact copies **and** the ones that only *look* or only *read* the same.

Written in Go. Ships as a single static binary with no runtime dependencies.

---

## Why this exists

I had an external drive holding years of photos, pulled together from several phones and a tangle of half-finished backups. The same picture existed three or four times over: once from the phone, once from a backup that resized it, once re-compressed by a messaging app, once as an iPhone HEIC next to its JPEG twin. Tens of gigabytes of the drive were duplicates, and no folder-by-folder cleanup was ever going to untangle it by hand.

I could have reached for an existing dedup tool. Instead I built the one I wanted — because "clean up my photo drive" turns out to be a genuinely interesting systems problem: read thousands of files fast, recognise the same photo across resizes and formats, and never, ever lose one to a bug.

`photocull` is the result, and it did the job on the real drive: **~1,900 photos, ~9 GB**, deduplicated down without a single permanent deletion.

### And then it turned out not to be about photographs

The same drive had the documents problem too — `informe.pdf`, `informe (1).pdf`, `informe final.pdf`, `informe final v2.docx`. Adding that looked like a second tool.

It was not, and the reason is the interesting part. Reading the finished code back, almost none of it was about pictures. `hashing.Distance` is an XOR and a popcount over a `uint64`. The union-find in `dedupe` reads that `uint64` and nothing else. `trash`, `report`, `osdialog` and the review server's permission model never mention images at all. **The only photo-specific thing in the entire tool was how that one 64-bit number got produced** — and text has an exact equivalent in SimHash: also 64 bits, also compared by Hamming distance.

So documents were not a fork or a second binary. They were one new interface with two implementations, and a `--kind` flag. The git history shows both halves: `v1.0.0` is the photo tool, and everything after it is the generalisation.

The name stayed. `photocull` doing documents is a mild misnomer, but renaming would touch imports in eleven packages to hide the one fact worth advertising — that the core turned out to be about fingerprints, not pixels.

## What it does

**For photographs**

- **Exact duplicates** — byte-for-byte identical files, found by SHA-256. Zero false positives; safe to remove on sight.
- **Similar photos** — the same image after resizing, re-compression or format conversion, found by a perceptual hash and grouped by visual distance.
- **HEIC/HEIF** — iPhone photos are decoded and matched alongside JPEG/PNG/GIF, so a HEIC and its JPEG export land in the same group. No system libraries required (libheif runs as WebAssembly).
- **Four ways to compare a pair** — side by side with synchronised zoom, a draggable wipe, a blink that alternates the two in place, and a server-rendered difference map. Each catches something the others miss; the blink in particular finds changes the eye cannot describe.

**For documents**

- **Text-aware matching** — PDF, `.docx`, `.xlsx`, `.pptx`, `.txt`, `.md` and `.csv` are read for their text and fingerprinted by what they *say*. A report and a later draft of it group together; so does a `.docx` and its own PDF export, because the fingerprint ignores the layout.
- **Word-level comparison** — opening a pair shows exactly which words differ, with the identical stretches collapsed to `··· 412 identical words ···` that expand on click. A thirty-page report with one changed adjective reads as *one change, 2 of 9,000 words*.
- **Everything else is still scanned** — a `.zip`, an `.mp3`, a legacy `.doc` is compared by content hash like anything else. A scan with blind spots is worse than no scan, because it is the blind spots you assumed were covered.

**For both**

- **A third tier for files nothing can read** — a scanned PDF with no text layer, a legacy `.doc`, an encrypted archive. Those are grouped by normalised name and near-equal size and marked **`related`**. It is a hint, not a finding: nothing is pre-selected, `clean` refuses to touch it, and its bytes are kept out of the "you could free N GB" headline. Guesses do not get to inflate the number you act on.
- **Safe by construction** — nothing is ever deleted permanently. Duplicates go to the **system recycle bin**, and only after you confirm. The default is always a dry run.
- **Its own window** — a native desktop window on Windows, the browser elsewhere, where you review by eye and decide.

## Install

### Windows — clone and double-click

Double-click **`run-photocull.cmd`** in the repository root. That is the whole setup: it builds the binary if there isn't one yet (Go 1.26+ needed for that first run, nothing after) and opens the app window.

Use the launcher rather than double-clicking `photocull.exe` directly — [A note on antivirus](#a-note-on-antivirus) explains why that matters more than it ought to.

### Everywhere else

```sh
# From source (Go 1.26+)
go install ./cmd/photocull

# Or grab a prebuilt binary from the Releases page — no dependencies to install.
```

## Usage

### Just run it — the app

Run photocull with no arguments — on Windows, double-click `run-photocull.cmd`. It opens a native window (WebView2 on Windows; falls back to the browser if that runtime is missing) where you **pick what to look through, a mode and a folder**, then review and act — no command line needed. A native folder picker is one click away.

```sh
photocull              # opens the app window
photocull --browser    # force the browser instead of a native window
```

The launcher forwards its arguments, so `run-photocull.cmd --browser` works the same way.

The three commands below are the same functionality from the terminal. All of them take `--kind photos` (the default) or `--kind docs`.

### See what's there — `scan`

`scan` only reads. It moves nothing.

```sh
photocull scan ~/Photos                          # exact duplicates
photocull scan ~/Photos --similar                # also resized / re-compressed copies
photocull scan ~/Documents --kind docs --similar # documents, by what they say
photocull scan ~/Photos --similar --json         # machine-readable report
```

```
Group 1  [similar]  3 files, 4.8 MB reclaimable
  KEEP    2019/summer/IMG_4021.jpg       4.9 MB  4032x3024  2019-07-14
  delete  backups/phone/IMG_4021.jpg     1.2 MB  1600x1200  2021-03-02
  delete  whatsapp/IMG-4021-copy.jpg     0.4 MB  1024x768   2021-08-19

Found 128 duplicate groups: 96 exact, 32 similar
241 files could be removed, freeing 9.3 GB
```

A documents scan adds the third tier, reported separately and never folded into the headline:

```
Group 97  [related]  2 files, 4.0 MB if they turn out to be copies
  KEEP    escaneos/informe.pdf           4.0 MB  2019-03-11
  review  escaneos/informe (1).pdf       4.0 MB  2019-03-11

Also found 6 groups where the names and sizes line up but photocull could not
read inside the files. About 31.2 MB might be freed after a look. Nothing is
pre-selected and clean will not touch them.
```

### Remove them — `clean`

Dry run by default; add `--confirm` to actually move duplicates to the recycle bin.

```sh
photocull clean ~/Photos                       # dry run: shows the plan, changes nothing
photocull clean ~/Photos --confirm             # asks before moving anything
photocull clean ~/Photos --confirm --yes       # no prompt (for scripts)
photocull clean ~/Documents --kind docs --similar
```

`related` groups are skipped whatever the flags say. There is no combination of arguments that recycles a file matched on nothing but its name.

### Review by eye — `serve`

For anything short of byte-identical, a look beats a file path.

```sh
photocull serve ~/Photos --similar
photocull serve ~/Documents --kind docs --similar
# → opens the app window on a fresh loopback port
```

### Thresholds and which copy to keep

`--threshold` is the Hamming distance at which two fingerprints count as the same thing, 0–64, lower being stricter. **It defaults per kind: 8 for photos, 6 for documents.** Those are different numbers because they measure different things — the arithmetic is in [`internal/hashing/simhash.go`](internal/hashing/simhash.go), but in short, unrelated documents sit around 32 apart and two invoices from one template sit around 18, so the useful band is narrower than for photographs.

Which copy to keep also defaults per kind, and the inversion is deliberate:

| Kind | Suggested keeper | Why |
|---|---|---|
| Photos | highest resolution, then larger, then **oldest** | the oldest copy is the original; each later one is a re-export that lost pixels |
| Documents | **newest**, then larger, then shallowest | the newest copy is the revision the person actually worked on |

Keeping the oldest document would mean suggesting you keep the draft and bin the final. Override either with `--strategy` (`default`, `resolution`, `oldest`, `newest`, `largest`, `document`).

## How it works

**Concurrent scan.** The scanner is a three-stage pipeline connected by channels: one goroutine walks the tree, a pool of workers (one per CPU core) reads and fingerprints files, and the results are funnelled back and aggregated. Each file is read from disk **exactly once** — an `io.TeeReader` feeds the SHA-256 hasher and the content extractor from the same byte stream — because on a drive with thousands of files the disk, not the CPU, is the bottleneck. A single corrupt file is reported and skipped, never fatal.

**One seam, two kinds.** `internal/fingerprint` isolates the only question that differs: *given this file's bytes, what 64-bit number describes what is in it?* An image extractor answers with a perceptual hash; a document extractor answers with a SimHash over the text. Everything downstream — grouping, stats, the recycle bin, the review server — is shared, because none of it was ever about pixels. The extractor is chosen once per run, which is a load-bearing invariant rather than an implementation detail: a perceptual hash and a SimHash sitting a short distance apart would mean nothing at all.

**Three tiers of confidence.** Exact matches come from a content hash. Similar ones come from a 64-bit fingerprint compared by Hamming distance. Related ones come from names and sizes, and only for files nothing could read inside. The first two are grouped with **union-find**, because similarity is not transitive: A can resemble B and B resemble C without A resembling C, yet all three are the same thing and belong in one decision. The third is deliberately *not*, because transitivity there would chain `informe` → `informe (1)` → `informe final` → an unrelated `informe` from another folder into one group of forty files.

**Text extraction is best-effort, and says so.** A scanned PDF has no text layer; a spreadsheet may be nothing but numbers; a legacy `.doc` is a compound binary nobody should parse for this. Rather than guessing, `internal/doctext` reports *why* it got nothing, and that sentence is what the review window shows in place of the missing text. Every failure mode there is a designed outcome.

**Word-level diffs.** `internal/textdiff` encodes each distinct word as a code point and diffs *those*, so the underlying character-diff library physically cannot cut a highlight mid-word. Whitespace is not compared at all, which is what lets a document be compared with its own PDF export: a PDF has no paragraphs, only glyphs at coordinates, so its text comes back hard-wrapped at a width the original never had.

**Safe deletion.** Removal always goes through the OS recycle bin (Shell32 on Windows, the FreeDesktop trash spec on Linux). There is no code path that deletes without an explicit human action in that same run.

## Project layout

```
run-photocull.cmd    Windows launcher: builds if needed, then opens the app
cmd/photocull        entry point
internal/scanner     concurrent walk + fingerprinting
internal/fingerprint the seam: bytes in, one 64-bit number out (photos | docs)
internal/hashing     SHA-256, perceptual hashing, SimHash
internal/imageutil   format decoding (incl. HEIC), thumbnails, image comparison
internal/doctext     text extraction: PDF, OOXML, plain text
internal/textdiff    word-level document comparison with collapsed context
internal/dedupe      grouping (union-find), the related pass, keep heuristics
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

The test suite runs entirely offline against small fixtures and **never touches the real recycle bin** — deletion is tested through an injected fake.

Coverage of the packages that decide what counts as a duplicate runs from 85% to 96%: `hashing` 96%, `textdiff` 93%, `report` 91%, `fingerprint` 91%, `dedupe` 89%, `scanner` 88%, `doctext` 87%, `pipeline` 86%. The review server sits at 84%, which includes the security guard that stops it serving any file outside the scanned set. `cli` is thin argument wiring at 33%, and the two lowest — `imageutil` at 72% and `trash` at 60% — are the packages whose remaining lines need a real image codec or a real recycle bin to reach.

CI additionally cross-compiles for Windows, Linux and macOS with `CGO_ENABLED=0`, which is what keeps the single-binary promise honest, and runs the suite under `-race` (there is no C compiler on the machine this was written on, so that check only ever happens there).

Some of the tests are worth reading as documentation of the design:

- `TestGroupRelatedDoesNotChainUnrelatedFiles` encodes the non-transitivity rule above, as a case that would otherwise silently produce a forty-file group.
- `TestPDFMatchesTheSameProseAsPlainText` is the reason PDF support was worth a dependency at all: it asserts that the same words land on the same fingerprint whichever format they arrive in.
- `TestScanHashesTheWholeFileWhateverTheExtractorReads` checks that a file whose extractor stopped early is still hashed in full — the one place the single-read invariant could break in silence, and the failure mode that would delete data.
- `TestWordDiffIgnoresLineBreaks` is the property that makes a `.docx` comparable with its own PDF export.

Fixture provenance is documented in [`testdata/README.md`](testdata/README.md). Office files are built by the tests rather than committed, because a test that constructs its own input states what it is testing and a committed binary cannot be reviewed in a diff.

## A note on antivirus

On Windows, double-clicking `photocull.exe` may do nothing whatsoever — no window, no error, no crash dialog. The binary is not broken. It is being killed.

A security suite scores the *context* a program is launched in, not only the file itself. `explorer.exe` → unsigned executable → binds a local port is the shape of the commonest malware delivery path there is, and photocull matches it exactly: unsigned, freshly built, and the very first thing it does is listen on a loopback port to serve its own interface. Measured on a machine running McAfee, the process starts, never gets as far as opening that port, and is terminated about five seconds in. The app is a `-H=windowsgui` build, so it has no console and nothing is printed anywhere.

`run-photocull.cmd` sidesteps this by putting `cmd.exe` in the chain instead, which is scored differently. Verified on that machine, same binary, minutes apart: launched by Explorer it dies at ~5 s having bound nothing; launched from the `.cmd` the window is up in well under a second. A renamed copy in a different folder failed identically, so this is about the launch path rather than one file's reputation.

It is a workaround, not a fix. The fixes are an antivirus exclusion for the folder, or an Authenticode-signed binary — and the reason a portable, unsigned tool ships with a launcher instead of just an `.exe` is worth knowing before you distribute one of your own.

## Notes

- Module path is `photocull`; rename to `github.com/<you>/photocull` when publishing.
- `--kind docs` pointed at a whole drive will SHA-256 everything on it, including a 40 GB VM image. There is no `--max-size`, on purpose: skipping files would break the guarantee that `exact` means exact. System directories (`windows`, `program files`, `appdata`, `node_modules` and friends) are skipped, and zero-byte files are ignored so every empty `.log` on the disk does not form one enormous "identical" group.
- Document text is compared as extracted, not as rendered. A `.docx` against its PDF export will show page numbers and hyphenation as differences; they are not changes to the content.

# Test fixtures

Everything here is written or generated for this repository. Nothing is
third-party content.

## Provenance

- **`exact/`, `similar/`, `corrupt/`** — synthetic JPEGs (coloured circles on a
  gradient), generated once. `exact/backup/original.jpg` is a byte-identical
  copy of `exact/original.jpg`; `similar/source_resized.jpg` and
  `similar/source_recompressed.jpg` are derived from `similar/source.jpg`;
  `corrupt/truncated.jpg` is a valid JPEG cut short.
- **`heic/`** — deliberately empty. No HEIC file is committed, to avoid
  redistributing third-party image data of unclear provenance. Drop any `.heic`
  file in and `TestDecodeHEIC` turns into a live check; otherwise it skips.
- **`docs/text/`** — prose written for this repository.
  `report_copy.txt` is a byte-identical copy of `report.txt`; `report_edited.txt`
  is the same document with three words changed; `unrelated.txt` is a different
  document of similar length and register.
- **`docs/binary/`** — a small ZIP and a byte-identical copy of it, standing in
  for every file photocull can only compare by content hash.

## What is *not* committed, and why

Office files (`.docx`, `.xlsx`, `.pptx`) are ZIP archives of XML, so the tests
build them in a temp directory instead. A test that constructs its own input
states what it is testing; a committed binary does not, and cannot be reviewed
in a diff. The same goes for the UTF-16 fixture, which is encoded from the UTF-8
one at test time.

The image fixtures are committed because the same argument runs the other way: a
JPEG cannot be written legibly in Go.

## Line endings

`.gitattributes` marks `testdata/**` as `-text`. Several of these files are
byte-identical pairs whose whole purpose is to hash the same, and line-ending
normalisation would break that between a Windows checkout and a Linux one — so a
test would pass locally and fail in CI for a reason nobody would guess from the
failure message.

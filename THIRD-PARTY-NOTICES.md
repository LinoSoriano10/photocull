# Third-party notices

photocull is distributed as a single static binary, which means every library
it uses is *inside* that binary. Most of those libraries ask, as a condition of
that, that their copyright notice travels with it. This file is that notice.

The list below is not the contents of `go.mod`. It is the set of modules that
`go list -deps ./cmd/photocull` reports as actually linked, checked separately
for `windows`, `linux` and `darwin`, so build-time and test-only dependencies
are excluded and nothing that ships is missing. The `Only on` column marks the
three that are platform-specific.

photocull's own code is licensed separately; see [LICENSE](LICENSE).

## The one that sets the license for everything

**`github.com/gen2brain/heic`** is MIT, but it `go:embed`s
`lib/heic.wasm.gz` — a WebAssembly build of the Rust crate
[`heic` v0.1.6](https://crates.io/crates/heic) by Imazen, which is
**AGPL-3.0-only OR a paid commercial license**. That blob is compiled into every
photocull binary, so photocull is a combined work with an AGPL component and
must be distributed under the AGPL. That is why the project is AGPL-3.0 and not
something more permissive.

Two details worth stating, because both are easy to get wrong:

- The crate is AGPL-3.0-**only**, not "or later". A combined work can therefore
  only be conveyed under exactly version 3, which is why photocull's own notice
  omits the usual "or, at your option, any later version".
- `gen2brain/heic` does *not* embed libheif, despite what a first reading of its
  README suggests. libheif is used only if you already have it installed as a
  system shared library, loaded at runtime through purego; the embedded fallback
  that ships in the binary is Imazen's pure-Rust decoder.

Source: <https://github.com/imazen/heic>

## Everything linked into the binary

Every copyright line below is quoted from that module's own license file, source
headers or `NOTICE`. Where a module states no copyright holder anywhere, this
says so rather than guessing one — an invented attribution is worse than a
missing one.

| Module | License | Copyright | Only on |
|---|---|---|---|
| `github.com/Bios-Marcel/wastebasket/v2` | MPL-2.0 | none stated; authored by Marcel Schramm (`Bios-Marcel`) | |
| `github.com/corona10/goimagehash` | BSD-2-Clause | Copyright (c) 2017, Dong-hee Na | |
| `github.com/ebitengine/purego` | Apache-2.0 | SPDX-FileCopyrightText: The Ebitengine Authors | |
| `github.com/gen2brain/heic` | MIT (embeds AGPL-3.0, above) | Copyright (c) 2024 gen2brain | |
| `github.com/gobwas/glob` | MIT | Copyright (c) 2016 Sergey Kamardin | windows, linux |
| `github.com/inconshreveable/mousetrap` | Apache-2.0 | none stated; authored by Alan Shreve (`inconshreveable`) | windows |
| `github.com/jchv/go-webview2` | MIT | Copyright (c) 2020 John Chadwick; some portions Copyright (c) 2017 Serge Zaitsev | windows |
| `github.com/jchv/go-winloader` | ISC | Copyright © 2021, John Chadwick \<john@jchw.io\> | windows |
| `github.com/ledongthuc/pdf` | BSD-3-Clause | Copyright (c) 2009 The Go Authors. All rights reserved. | |
| `github.com/nfnt/resize` | ISC | Copyright (c) 2012, Jan Schlicht \<jan.schlicht@gmail.com\> | |
| `github.com/sergi/go-diff` | MIT | Copyright (c) 2012-2016 The go-diff Authors. All rights reserved. | |
| `github.com/spf13/cobra` | Apache-2.0 | Copyright 2013-2023 The Cobra Authors | |
| `github.com/spf13/pflag` | BSD-3-Clause | Copyright (c) 2012 Alex Ogier. All rights reserved.<br>Copyright (c) 2012 The Go Authors. All rights reserved. | |
| `github.com/tetratelabs/wazero` | Apache-2.0 | Copyright 2020-2023 wazero authors | |
| `golang.org/x/sync` | BSD-3-Clause | Copyright 2009 The Go Authors | |
| `golang.org/x/sys` | BSD-3-Clause | Copyright 2009 The Go Authors | |
| `golang.org/x/text` | BSD-3-Clause | Copyright 2009 The Go Authors | |

The full text of each is in that module's own `LICENSE` file, which `go mod
download` puts in your module cache; `go list -m -f '{{.Dir}}' <module>` prints
where.

### Required NOTICE, per Apache-2.0 §4(d)

`github.com/tetratelabs/wazero` ships a `NOTICE` file, and Apache-2.0 requires
its contents to travel with any distribution. It reads, in full:

```
wazero
Copyright 2020-2023 wazero authors
```

No other Apache-2.0 dependency here carries a `NOTICE`.

## Notes on the less obvious ones

**`wastebasket` is MPL-2.0**, which is the only copyleft here besides the AGPL
blob. It is file-level copyleft: its own files stay under the MPL and their
source has to be available, but it places no conditions on photocull's code.
Combining it with the AGPL is explicitly permitted by
[MPL-2.0 §3.3](https://www.mozilla.org/en-US/MPL/2.0/#distribution-of-a-larger-work).
It is here because it is the recycle bin — the reason photocull can promise that
nothing is ever deleted permanently.

**`wazero` and `purego` are only in the tree because of HEIC.** wazero runs the
embedded WebAssembly decoder; purego is what lets the same package try a system
libheif first without CGo. Removing HEIC support would remove all three, and
with them about 7 MB of binary and the AGPL obligation.

**The Go standard library** is BSD-3-Clause, Copyright 2009 The Go Authors, and
is statically linked like everything else.

## Reproducing this list

```sh
go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/photocull |
  sort -u
```

Run it once per `GOOS`. If it prints a module that is not in the table above,
this file is out of date and the omission is a license violation, not a
formatting problem.

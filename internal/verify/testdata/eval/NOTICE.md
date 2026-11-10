# Eval corpus provenance

`pruefset.json` is copied verbatim from the reference implementation
`https://github.com/carsteneu/proofboy` at commit
`297702c35068b71fa86b237d89a8dc6c90502d65`, file `tests/data/pruefset.json`.

It is the contract of this port: the Go verifier must reach the same verdicts,
including the defect classification, as the Python reference for every case it
covers. The corpus was copied, not rebuilt, so the two implementations cannot
drift apart politely.

Upstream project: proofboy, MIT License, Copyright (c) 2026 carsten_eu. The full
license text follows, as MIT requires it to accompany copies.

```
MIT License

Copyright (c) 2026 carsten_eu

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## What is not covered, and why

The reference corpus also carries seventeen cases for the mathematical claim
types (`halt`, `searched`, `cycle`, `ident`, `coloring`, `lean`). Those types
are explicitly out of scope for this port: the daemon stays a static Go binary
without a Lean toolchain and without a runtime simulator, so the port has no
checker for them. `eval_test.go` skips those cases by name and fails if the
skip list and the corpus ever disagree.

## How the cases refer to the fixture

The reports embed commit hashes of a deterministically built fixture
repository (fixed content, fixed identity, fixed dates, sha1 object format).
`fixture_test.go` rebuilds that repository in Go and asserts the anchors this
corpus relies on, so a case that quietly stops matching fails loudly instead
of passing against the wrong object.

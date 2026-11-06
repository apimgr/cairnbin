# SPEC.md

Project-specific overrides to `AI.md`. Per the file hierarchy (SPEC.md > AI.md
> global CLAUDE.md), entries here supersede the named `AI.md` section without
editing the read-only `AI.md` file itself.

## PART 32 — GUI toolkit override

`AI.md` PART 32 "GUI Mode Requirements" names native toolkits (GTK4/Qt6 on
Linux, Cocoa on macOS, Win32/WinUI on Windows) with Gio (`gioui.org`) or Fyne
(`fyne.io/fyne/v2`) documented as accepted Go-native alternatives.

All of the above were verified this project to require cgo:

- GTK4/Qt6/Cocoa/Win32-WinUI bindings: cgo by nature (wrapping native C/C++/
  Objective-C toolkits)
- Gio v0.10.2: `import "C"` found in its windowing/GPU backend dependency
  tree
- Fyne v2.8.1: `import "C"` found in its GLFW/OpenGL dependency tree

Every one of these contradicts the absolute, non-negotiable
`CGO_ENABLED=0`, pure-Go-only rule in PART 2/PART 7 (see
`.claude/rules/binary-rules.md`, `.claude/rules/project-rules.md`).

**Override:** the GUI toolkit for this project is
**`github.com/gogpu/ui`** (built on `github.com/gogpu/wgpu`'s pure-Go WebGPU
backend, via `github.com/go-webgpu/goffi`'s dlopen/fakecgo FFI layer —
the same category of technique as `ebitengine/purego`).

Verified via real Docker builds in `casjaysdev/go:latest`
(`CGO_ENABLED=0`, `GOFLAGS=-buildvcs=false`):

- Compiles cleanly under `CGO_ENABLED=0`
- Zero `import "C"` anywhere in its resolved dependency tree
  (`gogpu/*`, `go-webgpu/*`, `coregx/*`)
- Cross-compiles successfully for all 4 required OS families:
  `linux/amd64`, `linux/arm64`, `windows/amd64`, `darwin/amd64`,
  `freebsd/amd64`
- Renders via native OS graphics APIs (Vulkan/Metal/DX12/GLES) called
  through dlopen-based FFI at runtime — no bundled Rust/C library, no
  compile-time cgo linkage

**Accepted risk:** `gogpu/ui` is a pre-1.0 library (currently resolves to
v0.1.54) with no long-term track record and an API that may change.
This was an explicit, informed project-owner decision, made after Gio and
Fyne were both disproven as cgo-free alternatives — accepted in preference
to shipping a cgo-dependent GUI (a harder constraint violation) or dropping
GUI mode from scope.

Everywhere `AI.md` PART 32 references Gio, Fyne, or native OS toolkits for
the GUI display mode, `github.com/gogpu/ui` is the actual toolkit in use for
this project.

**Update (2026-09-13):** `AI.md` PART 32 was updated upstream to mandate
`github.com/gogpu/ui` directly, confirming this override rather than
conflicting with it. Upstream also documents that `gogpu/gogpu`'s windowing
layer has no BSD backend yet — GUI mode is compiled out and unavailable on
`freebsd`/`netbsd`/`openbsd`, which fall back to TUI (or CLI with no TTY).
This section is kept for historical context (the cgo verification work that
led to the decision) but no longer overrides `AI.md`, which now states the
same toolkit choice natively.

## PART 32 — `display.mode: auto` resolution

`AI.md` PART 32 states the `auto` rule twice, and the two statements conflict:

- the `display.mode` table: `auto` → "Detect: GUI if display, TUI if
  terminal, error if neither"
- the conditions table: "Interactive terminal + config flags only → TUI mode"

`AI.md` is read-only and carries no way to break a tie, so this is recorded
here as an override, decided by the project owner on 2026-09-27.

**Override:** when `display.mode` is `auto`, the GUI wins whenever a display
is actually available; the TUI is selected only when no display is present.
`AI.md`'s conditions-table row is read as governing the terminal fallback
path rather than as a competing statement of preference.

This changes the no-arg default for desktop users running a binary built with
`-tags gui`: they now get the GUI window instead of the TUI. Untagged release
builds are unaffected, since GUI code is not linked in at all there and
`auto` always falls through to the pre-existing TUI/plain logic.

Two constraints hold regardless of which mode is selected, and are not part
of the tie-break:

- a command argument always beats both display modes — `cairnbin-cli list`
  prints a list even with `display.mode: gui` set
- `display.mode: gui` is an explicit request, so when no display exists it
  reports `gui_unavailable` and exits rather than silently degrading to the
  TUI the user did not ask for

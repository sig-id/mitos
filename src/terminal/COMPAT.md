# Terminal Core Compatibility

Status: initial integration boundary.

- `src/terminal/core.zig` is the only product-facing terminal API.
- `src/terminal/ghostty.zig` owns the future `libghostty-vt` integration point.
- `third_party/ghostty/` is reserved for the pinned Ghostty source tree.
- Current inspected Ghostty pin: `46d54ed673a004df09078bee56e809421a82370e`.
- Build configuration exposes `-Dghostty-source=<path>` so packaging can point at
  a vendored or externally managed Ghostty checkout without changing product
  modules.
- `zig build ghostty-check -Dghostty-source=<path>` validates that a checkout has
  the expected libghostty-vt source/API files and, when `.git` metadata exists,
  matches the inspected commit.
- `zig build ghostty-lib -Dghostty-source=<path> -Dwith-ghostty-lib=true` builds
  Ghostty's `libghostty-vt` artifacts from that checkout without making normal
  `zig build test` fetch or build upstream dependencies.
- `zig build terminal-native-test -Dghostty-source=<path>
  -Dwith-ghostty-lib=true` builds Ghostty and runs a libc-linked native
  adapter test against the produced `libghostty-vt.so`.

The compatibility backend intentionally does not expose any upstream Ghostty
symbols. The native adapter keeps raw handles and borrowed upstream data inside
`src/terminal/ghostty.zig` and converts snapshots to owned `core.zig` values
before returning.

`NativeGhosttyTerminal` is a guarded C-ABI adapter for compatible
`libghostty-vt` shared libraries. It requires a libc-linked consumer so Zig uses
the platform dynamic loader rather than the no-libc ELF loader. Use
`ManagedTerminal` with `.backend = .auto` to select the native adapter when
`MITOS_LIBGHOSTTY_VT` or an explicit native library path is configured, otherwise
it falls back to the deterministic compatibility backend.

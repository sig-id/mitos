# Ghostty Source Pin

This directory is the vendoring point for the pinned Ghostty / `libghostty-vt`
source tree used by Mitos.

The product-facing API lives in `src/terminal/core.zig`. Direct Ghostty symbols
must remain contained in `src/terminal/ghostty.zig`; other modules should use
`TerminalCore` and owned snapshot/effect types only.

Until the upstream source tree is vendored, `src/terminal/ghostty.zig` provides a
deterministic compatibility backend for terminal golden tests and for adapter
work that only needs the stable Mitos terminal contract.

Use `zig build ghostty-check -Dghostty-source=<checkout>` to validate an external
checkout against `PIN`. Use `zig build ghostty-lib -Dghostty-source=<checkout>
-Dwith-ghostty-lib=true` to build Ghostty's `libghostty-vt` artifacts from that
checkout.

Use `zig build terminal-native-test -Dghostty-source=<checkout>
-Dwith-ghostty-lib=true` to run Mitos' native C-ABI adapter test against the
produced shared library.

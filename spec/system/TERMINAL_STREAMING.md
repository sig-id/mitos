# Terminal streaming

Status: normative local transport contract. Behavioral statements below are
requirements. [ADR 0001](../ADRs/0001_TERMINAL_RENDERER.md) records renderer selection
and historical validation; [durable runtime](DURABLE_RUNTIME.md) defines future
process-host separation and broader receipt semantics.

## Output transport

The browser connects to `/api/sessions/{id}/stream?offset=N`. The server sends
JSON frames `{data,next,status}`; `data` contains base64-encoded original PTY
bytes, at most 64 KiB per frame. After xterm's write callback completes, the
browser records `next` and replies `{next:N}`. There is at most one output frame
awaiting acknowledgment, so background tabs cannot accumulate an unbounded queue.

PTY output is appended to the existing output file. A coalescing notification
wakes readers immediately after writes or lifecycle updates; there is no periodic
output poll. Each connection reads at its own offset. Slow viewers wait on disk
replay instead of blocking the PTY or allocating a transcript queue.

Output reconnects from the last rendered byte. Input requests remain serialized
HTTP mutations with their existing duplicate-key window; ambiguous input is not
retried. A lost output connection disables input until output reconnects. Leaving
the page closes the socket, and back/forward restoration rebuilds the terminal.
Final lifecycle state is delivered after all retained output has been drained.

Application heartbeats are sent every ten seconds. Frame writes and acknowledgments
have fifteen-second deadlines. A browser watchdog reconnects silent connections.
WebSocket uses a separate upgraded connection, avoiding the HTTP/1 connection
limit that would let several persistent fetch streams occupy input connections.
Shutdown explicitly cancels upgraded connections, which `http.Server.Shutdown`
does not otherwise own. Origin and loopback Host restrictions still apply.

The existing bounded `/output` read API remains available for tools and tests.
Managed build output retains its separate polling path. Process ownership and
terminal rendering remain independent from transport and agent orchestration.


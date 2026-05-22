#!/usr/bin/env python3
"""End-to-end smoke test for the mitos-term sidecar.

Spawns the binary, plays the role of the BEAM orchestrator: speaks the
{packet, 4} protocol on stdin/stdout, runs `echo` inside a PTY, asks for a
snapshot, then shuts down. Reports what it observed.
"""
import os
import struct
import subprocess
import sys
import time

# Stream tags (must match src/sidecar/protocol.zig).
RAW, EVENT, SNAPSHOT, CONTROL = 0x01, 0x02, 0x03, 0x10

# --- Minimal MessagePack encoder (subset we need) --------------------------

def mp_str(s):
    b = s.encode("utf-8")
    n = len(b)
    if n <= 31: return bytes([0xa0 | n]) + b
    if n <= 0xff: return b"\xd9" + bytes([n]) + b
    if n <= 0xffff: return b"\xda" + struct.pack(">H", n) + b
    return b"\xdb" + struct.pack(">I", n) + b

def mp_uint(v):
    if v <= 0x7f: return bytes([v])
    if v <= 0xff: return b"\xcc" + bytes([v])
    if v <= 0xffff: return b"\xcd" + struct.pack(">H", v)
    if v <= 0xffffffff: return b"\xce" + struct.pack(">I", v)
    return b"\xcf" + struct.pack(">Q", v)

def mp_map(pairs):
    n = len(pairs)
    if n <= 15: hdr = bytes([0x80 | n])
    elif n <= 0xffff: hdr = b"\xde" + struct.pack(">H", n)
    else: hdr = b"\xdf" + struct.pack(">I", n)
    body = b"".join(k + v for k, v in pairs)
    return hdr + body

def mp_array(items):
    n = len(items)
    if n <= 15: hdr = bytes([0x90 | n])
    elif n <= 0xffff: hdr = b"\xdc" + struct.pack(">H", n)
    else: hdr = b"\xdd" + struct.pack(">I", n)
    return hdr + b"".join(items)

# --- Minimal MessagePack decoder -------------------------------------------

class Dec:
    def __init__(self, buf): self.b, self.i = buf, 0
    def _u8(self): v = self.b[self.i]; self.i += 1; return v
    def _read(self, n): r = self.b[self.i:self.i+n]; self.i += n; return r
    def decode(self):
        h = self._u8()
        if h & 0x80 == 0: return h
        if h & 0xe0 == 0xe0: return struct.unpack("b", bytes([h]))[0]
        if h & 0xf0 == 0x80:
            n = h & 0x0f; return {self.decode(): self.decode() for _ in range(n)}
        if h & 0xf0 == 0x90:
            n = h & 0x0f; return [self.decode() for _ in range(n)]
        if h & 0xe0 == 0xa0:
            n = h & 0x1f; return self._read(n).decode("utf-8", errors="replace")
        if h == 0xc0: return None
        if h == 0xc2: return False
        if h == 0xc3: return True
        if h == 0xc4: n = self._u8(); return bytes(self._read(n))
        if h == 0xc5: n = struct.unpack(">H", self._read(2))[0]; return bytes(self._read(n))
        if h == 0xc6: n = struct.unpack(">I", self._read(4))[0]; return bytes(self._read(n))
        if h == 0xcc: return self._u8()
        if h == 0xcd: return struct.unpack(">H", self._read(2))[0]
        if h == 0xce: return struct.unpack(">I", self._read(4))[0]
        if h == 0xcf: return struct.unpack(">Q", self._read(8))[0]
        if h == 0xd0: return struct.unpack(">b", self._read(1))[0]
        if h == 0xd1: return struct.unpack(">h", self._read(2))[0]
        if h == 0xd2: return struct.unpack(">i", self._read(4))[0]
        if h == 0xd3: return struct.unpack(">q", self._read(8))[0]
        if h == 0xd9: n = self._u8(); return self._read(n).decode("utf-8", errors="replace")
        if h == 0xda: n = struct.unpack(">H", self._read(2))[0]; return self._read(n).decode("utf-8", errors="replace")
        if h == 0xdb: n = struct.unpack(">I", self._read(4))[0]; return self._read(n).decode("utf-8", errors="replace")
        if h == 0xdc: n = struct.unpack(">H", self._read(2))[0]; return [self.decode() for _ in range(n)]
        if h == 0xdd: n = struct.unpack(">I", self._read(4))[0]; return [self.decode() for _ in range(n)]
        if h == 0xde: n = struct.unpack(">H", self._read(2))[0]; return {self.decode(): self.decode() for _ in range(n)}
        if h == 0xdf: n = struct.unpack(">I", self._read(4))[0]; return {self.decode(): self.decode() for _ in range(n)}
        raise ValueError(f"unsupported type 0x{h:02x}")

# --- Framing ---------------------------------------------------------------

def send_frame(p, stream, body):
    payload = bytes([stream]) + body
    p.stdin.write(struct.pack(">I", len(payload)) + payload)
    p.stdin.flush()

def recv_frame(p, timeout_s=5.0):
    p.stdout.flush()
    hdr = _read_exact(p.stdout, 4, timeout_s)
    if hdr is None: return None
    n = struct.unpack(">I", hdr)[0]
    body = _read_exact(p.stdout, n, timeout_s)
    return body[0], body[1:]

def _read_exact(stream, n, timeout_s):
    deadline = time.time() + timeout_s
    out = b""
    while len(out) < n:
        chunk = stream.read(n - len(out))
        if not chunk:
            if time.time() > deadline: return None
            time.sleep(0.01); continue
        out += chunk
    return out

# --- Test driver -----------------------------------------------------------

def main():
    binary = sys.argv[1] if len(sys.argv) > 1 else "zig-out/bin/mitos-term"
    p = subprocess.Popen(
        [binary], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=sys.stderr, bufsize=0,
    )

    # 1) hello
    send_frame(p, CONTROL, mp_map([(mp_str("cmd"), mp_str("hello"))]))
    s, body = recv_frame(p)
    msg = Dec(body).decode()
    print(f"[event] {msg}")
    assert msg.get("type") == "hello", msg

    # 2) open: run /bin/sh that echoes a tagged line, then exits.
    open_cmd = mp_map([
        (mp_str("cmd"), mp_str("open")),
        (mp_str("argv"), mp_array([mp_str("/bin/sh"), mp_str("-c"), mp_str("printf 'mitos-term ok\\n'; sleep 0.05")])),
        (mp_str("cols"), mp_uint(80)),
        (mp_str("rows"), mp_uint(24)),
        (mp_str("idle_ms"), mp_uint(50)),
    ])
    send_frame(p, CONTROL, open_cmd)

    saw_opened, saw_raw, saw_idle, saw_exit = False, False, False, False
    raw_text = b""
    deadline = time.time() + 5
    while time.time() < deadline:
        f = recv_frame(p, timeout_s=2.0)
        if f is None: break
        stream, body = f
        if stream == RAW:
            saw_raw = True
            raw_text += body
        elif stream == EVENT:
            msg = Dec(body).decode()
            print(f"[event] {msg}")
            t = msg.get("type")
            if t == "opened": saw_opened = True
            elif t == "idle": saw_idle = True
            elif t == "exit": saw_exit = True; break
            elif t == "error": print(f"[error] {msg}"); break
        elif stream == SNAPSHOT:
            snap = Dec(body).decode()
            print(f"[snap] cols={snap['cols']} rows={snap['rows']} cells={len(snap['cells'])}")

    p.wait(timeout=3)
    print(f"raw bytes received: {raw_text!r}")
    ok = saw_opened and saw_raw and saw_exit and b"mitos-term ok" in raw_text
    print(f"opened={saw_opened} raw={saw_raw} idle={saw_idle} exit={saw_exit}")
    sys.exit(0 if ok else 1)

if __name__ == "__main__":
    main()

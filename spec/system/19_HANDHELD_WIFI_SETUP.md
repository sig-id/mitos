# Portable handheld Wi-Fi setup

Status: Normative subsystem contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

Status: local AP portal implemented 2026-09-22, superseding acoustic transfer as
**From phone**. Direct watch keyboard remains available. Native parser/DNS/touch
and mobile-browser tests pass. Physical phone captive-window/QR handoff remains
an explicit hardware acceptance check, not a claimed result of simulation.

## Default: protected local setup hotspot

1. On the watch, choose a network and **From phone**.
2. Scan its standard Wi-Fi QR to join a temporary `Mitos-Setup-…` WPA2 hotspot.
   The watch also displays its SSID and fresh random 16-character password.
3. A captive page may open automatically. Otherwise **Open setup** shows a second
   URL QR and the literal local HTTP address. Stay connected despite “No internet”.
4. Enter/paste the target network password and tap **Connect watch** once. An
   incorrect password stays editable; actual DHCP and durable profile storage
   determine success. AP and station share a radio, so channel changes can drop
   the phone. The page tells users to check the watch/rejoin setup; a failed HTTP
   request never implies successful joining.
5. Cancel closes setup immediately when the worker processes cancellation. An
   idle session expires after five minutes. Success retains the AP for ten seconds
   so the phone can receive status, then closes it. Done can close it sooner.

The phone needs no internet, PWA installation, audio permissions, or public host.
The ESP32 owns AP+STA, DHCP, captive DNS, local HTTP, session authorization,
joining, retries, profile storage and cleanup. It serves HTTP only to its AP
address/subnet; no NAT, forwarding, Studio proxy, token or pairing APIs are added.
HTTP uses the temporary WPA2 link. Every session rotates the AP password and an
independent 128-bit request token. Joins require the current session, canonical
Host/Origin, JSON content type, bounded body and exact selected SSID. Duplicate,
stale, cross-origin, malformed and unsolicited submissions are rejected.
No credentials appear in URLs, logs, storage in the browser, or a Mitos request.
Browser assets and status use no-store and a restrictive same-origin CSP.

The automatic entry uses captive DNS and HTTP probe redirects, including a
nonempty redirect response for iOS. DNS supports ordinary and bounded EDNS(0)
queries. The HTTP access check accepts both native IPv4 and IPv4-mapped IPv6
socket addresses: ESP-IDF's dual-stack listener uses the latter for IPv4 clients.
It still restricts the destination to the AP address and peers to its subnet.
Phones control whether to auto-open a window or show “Sign in to Wi-Fi”; HTTPS
and HSTS pages are not intercepted. Keep the local HTTP URL/QR fallback.

## Device-local portal API

| Request | Contract |
| --- | --- |
| `GET /api/status` | JSON `ssid`, ephemeral `token`, `stage` (`ready`, `joining`, `error`, `connected`), and a short `message`. |
| `POST /api/join` | Same-origin JSON `{ "ssid": "selected network", "password": "…" }`, with `X-Setup-Token` from status. Maximum body 512 bytes; no additional/duplicate fields. |
| Join response | `202 { "accepted": true }` means an attempt was queued, never that connection succeeded. Poll status for the result. |
| Rejection | `400` invalid payload; `403` wrong host/origin/token; `408` incomplete body; `409` expired/cancelled/busy/completed session. |

There is no cross-origin access. Credential POSTs are valid only for the network
selected on the watch, in its armed setup session. A successful status requires
association, DHCP and profile storage; authentication/network failures allow
retry until the session deadline. Watch cancellation invalidates submissions
before waiting for HTTP/DNS teardown. The phone retains input through a temporary
connection loss, clears it on confirmed success or page exit, and persists none.

## Mitos ownership and future deployment

Mitos owns the canonical mobile portal at `internal/wifisetup/portal/`.
`tools/build-wifi-setup.sh` emits a separate `…-portal` firmware asset bundle next
to the optional public acoustic sender. Do **not** host the local portal as the
public `/wifi-setup/` replacement: its `/api/status` and `/api/join` are device-local.
Future releases should version and distribute this firmware asset bundle with
Mitos, retaining the static local dependency-free contract.

The sibling firmware vendors these three exact assets under `firmware/main/portal`
and embeds them in the application image. Run `python tools/sync-wifi-portal.py`
from esp-notify after editing them; `--check` verifies equality. Standalone firmware
builds do not depend on a sibling checkout or a running backend.

Tests: `CHROMIUM_PATH=/path/to/chromium npm run test:portal` in
`tools/wifi-sender` starts a local device API fixture and exercises validation,
password reveal, wrong-password retry, channel loss, confirmed connection,
secret clearing, local-only requests and a 320px viewport. This is a simulation
of the device API; real HTTP/radio acceptance must be recorded separately.

## Optional acoustic implementation (retained, not the default watch flow)

The prior sender, OFDM codec, and tests remain available for future experimentation.
No new Cloudflare deployment is required by the hotspot flow. The historical
publication and optional deployment instructions are in
[the deployment guide](../../deploy/wifi-setup/README.md).

### Acoustic phone sender

Provide a small mobile setup page, installable/cacheable for offline use, with
local SSID/password entry, password paste/reveal, Play, Stop and Replay. It must
import the watch's ephemeral session QR, encode a bounded versioned payload,
encrypt/authenticate it and synthesize an OFDM waveform in the browser. Provide
an explicit Play gesture for browser audio startup. Do not request the phone's
microphone; receipt/connection success is displayed by the watch. Playback
completion alone must not claim success.

Host/version the page and codec assets as part of Mitos tooling, with a separately
deployable static build. It must be accessible when the watch cannot reach Mitos:
cellular access to an approved public static origin or an already cached offline
installation must suffice. Do not rely on the home Studio's loopback/LAN address
as the only entry point. Do not expose Studio administration to serve this page.

All secret handling is client-local. Import a fresh 256-bit session secret from
the QR URL fragment, remove the fragment from history after import, and encrypt
with a standard authenticated cipher interoperable with ESP-IDF (candidate:
AES-GCM). Bind the protocol version, watch session and intended network. Do not
send secrets in URLs to the server, requests, telemetry, error reports or logs.
No third-party analytics or remote code; cache only versioned static assets.
Credentials/session keys must not persist in localStorage or service-worker
caches. Use a restrictive content policy and clear transient state when finished.

Use a shared protocol definition and test vectors with esp-notify: byte encoding,
maximum lengths, nonce/authentication rules, replay lifetime, OFDM parameters,
FEC and framing. Browser sample rates vary; resample explicitly as required by
the chosen profile. The implementation uses a fixed 16 kHz OFDM profile with 48 differential-BPSK
data carriers, four clock pilots, cyclic prefix, repetition combining, CRC32 and
AES-256-GCM. The exact interoperable wire format is documented in esp-notify.
Quiet/Quiet.js informed feasibility research; no unqualified port is shipped.

Wi-Fi payloads grant only Wi-Fi configuration for an explicitly armed watch
session. They do not modify the device's Mitos origin, trust roots, pairing token
or pending resolutions. No acoustic payload calls the voice transcription API.
An explicitly armed acoustic session authorizes transfer; a valid matching frame can initiate joining without another hold or
confirmation. Cancellation/timeout/acceptance retires the session.

## Responsibility and connectivity

The firmware owns scanning, keyboard, microphone/DSP, saved networks, NVS updates,
reconnect, and honest Wi-Fi-versus-Mitos status. Mitos does not store or broker
Wi-Fi passwords. The existing HTTPS snapshot/resolution/voice endpoints need no
new credential-transport API for Wi-Fi changes.

Joining a travel network does not make the existing home LAN Mitos origin
reachable. Document a stable, device-reachable HTTPS origin and matching trust
configuration for remote operation, preserving the connector's scoped token and
TLS boundaries. DNS alone is insufficient without routing. Do not assume a
phone hotspot forwards the phone's VPN connection to clients. Remote exposure,
relay/tunnel deployment and certificate management remain separate setup work;
do not silently publish the user's running Studio or weaken TLS verification.

## Acceptance

- Page works from its cached installation without contacting Mitos; loading a
  previously uncached page while fully offline is not claimed to work.
- Browser tests prove no credential/session-secret network requests or persistent
  storage, correct fragment cleanup, bounded field validation and stop/replay.
- Browser-generated frames match native/firmware decoder test vectors, including
  maximal credentials, corruption, wrong keys, stale sessions and duplicates.
- Physical Android/iOS playback into the Waveshare ES8311 path is measured before
  announcing supported phones, distance, noise tolerance or transmission time.
- Watch setup works with Mitos offline and leaves pairing and journal intact.
- Connection UI distinguishes Wi-Fi association/IP from authenticated Mitos
  reachability. The phone UI never reports delivery merely from audio playback.

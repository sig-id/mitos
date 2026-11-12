# Phone Wi-Fi sender deployment

**Current watch default:** a protected local hotspot serves the portal directly.
It needs no public deployment. Canonical portal files are in
`internal/wifisetup/portal`; the build script emits them separately as
`dist/wifi-setup-site-portal` for firmware embedding. Keep the portal APIs on the
watch. This deployment tool is separate from the
[Mitos client API](../../spec/system/05_INTERFACES_AND_INTEGRATIONS.md#client-operations-and-security).

The remaining instructions describe the retained optional acoustic sender.


The sender is implemented in `internal/wifisetup/assets` and can be deployed
without Studio or device API access. It imports an ephemeral key from a watch
QR fragment and encrypts/encodes Wi-Fi credentials in the browser. No credential
POST API exists. The page needs a trusted HTTPS origin for Web Crypto and
service-worker offline caching. Keep this origin stable between releases.

Build a Cloudflare Pages/static-host artifact from the Mitos root:

```sh
./tools/build-wifi-setup.sh
```

Publish `dist/wifi-setup-site` as the output directory. The entry point is
`https://YOUR_ORIGIN/wifi-setup/`. `_headers` supplies the browser policy on
Cloudflare Pages. Configure equivalent headers on other static hosts. Disable
injected analytics/third-party scripts on this origin. Include this build and
publication in the Mitos deployment pipeline; it has no Node/build dependencies.

For a standalone service:

```sh
go run ./cmd/mitos-wifi-setup --addr 127.0.0.1:8747
```

Localhost permits development Web Crypto. A public reverse proxy should terminate
HTTPS at a trusted certificate and forward only this static server. Alternatively
supply `--cert`/`--key` to the standalone server. The device HTTPS connector also
serves these exact static resources at `/wifi-setup/`; its authenticated `/api`
routes retain their existing checks. Serving the assets does not expose Studio.

Set the handheld's `CONFIG_ESP_NOTIFY_WIFI_SETUP_URL` to that HTTPS entry point,
or provision the `device/setup_url` NVS key using `tools/provision.py --setup-url`
in esp-notify. The provisioned value wins over the firmware default. Existing
Mitos URL/CA/pairing are independent of this sender URL.

## Temporary testing deployments

For board testing, start the standalone static server on `127.0.0.1:8747`, then
run `cloudflared tunnel --no-autoupdate --url http://127.0.0.1:8747`. Open
`/wifi-setup/` on the HTTPS hostname printed by the tunnel. Forward only the
standalone static sender; keep the Mitos backend on its separate listener.

The temporary hostname lasts only while the tunnel is running. Use a stable
Pages or custom-domain deployment for the released product.

The page caches versioned assets for offline use. First visit requires connectivity;
a previously cached copy can operate without reaching Mitos or the static host.
Credentials and session keys are not stored in browser storage or asset caches.
The watch remains the source of delivery/connection status; audio playback ending
does not prove reception. The session expires on the watch after two minutes.

A portable sender does not make a home LAN Mitos backend reachable while travelling.
Deploy a separate stable HTTPS device endpoint with appropriate routing and trust
if approvals must work away from that LAN. No endpoint changes are sent over sound.

## Verification

Run Go route tests with `go test ./internal/wifisetup ./internal/device`.
For the actual browser flow, start the standalone server, then:

```sh
cd tools/wifi-sender
npm ci
npx playwright install chromium
npm test
```

`CHROMIUM_PATH` can select an installed Chromium executable.
`WIFI_SENDER_URL=https://YOUR_ORIGIN/wifi-setup/ npm test` exercises a deployment,
including its cached offline flow. Test credentials are synthetic. Firmware/native
interoperability tests run in the sibling esp-notify repository and consume this
repository's actual `modem.mjs` encoder. Keep the shared protocol version in sync.

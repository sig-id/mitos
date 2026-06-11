# poc/ — proof-of-concept run viewer

The current proof-of-concept web surface for the mitos daemon, served at `/`
by `Mitos.Web.Router` (resolution: app config `:webapp_dir`,
`MITOS_WEBAPP_DIR`, then this directory).

It follows the product surfaces in `spec/01_PRODUCT.md` §7.2 (run viewer) and
§9.4 (approval UX), and `spec/07_GENTIX_FACILITATOR.md` §6 (workloads /
proof page):

- `#/runs` — run lifecycle list over `GET /runs` (live sessions merged with
  the historical `.runs/` trace store)
- `#/runs/:id` — evidence viewer: live terminal (WS) with semantic steering,
  typed-event timeline, approval cards, run report
- `#/new` — launch surface (`POST /agents`, raw-argv escape hatch via
  `POST /sessions`)
- `#/workloads` — packaged capabilities (`GET /workloads`,
  `POST /workloads/:id/execute`) with a deliverable + evidence proof panel

No build step: `index.html` plus `assets/app.{css,js}` (vanilla ES module);
xterm.js comes from a CDN. This is explicitly a POC — it proves the viewer
surfaces against the real backend contract and is expected to be replaced by
the production viewer. The first-generation terminal-centric console it
replaced is in git history (`beam/priv/static/index.html`).

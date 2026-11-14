# UI instructions

Use `<mitos-instructions>` for contextual help throughout the web UI. Its source is Markdown, rendered with `MitosFormat.markdown`, the same renderer used for agent output. It renders into light DOM; there is no shadow root or separate styling system.

Keep labels, values, errors, progress, and consequences needed to make a decision visible. Delete redundant copy. Put short explanations beside the relevant heading or field label, and longer instructions in a popover.

```html
<div class="instruction-label">
  <label for="timezone">Time zone</label>
  <mitos-instructions label="Time zone">
    Defaults to this device’s local time zone.
  </mitos-instructions>
</div>
<input id="timezone" name="timezone">

<mitos-instructions label="Scheduled workflows" mode="popover">
Each run inherits the project’s execution limits.

Manage saved workflows in [Background jobs](/services).
</mitos-instructions>
```

Do not put the component inside a `<label>`: its help button must remain separate from the labelled input. Use a specific `label` so the button has a useful accessible name.

Dynamic screens use the same component:

```js
const instructions = MitosInstructions.create(
  'Includes **planner rounds**, workers, and retries.',
  'Agent attempts'
);
container.append(instructions);
instructions.value = 'Updated Markdown instructions.';
```

Set `.value` to update the Markdown; replacing `.textContent` on an upgraded element would remove its controls. `label` and `mode` attribute changes also update the component. Use `mode="popover"` to force click-to-open help. The default uses a tooltip for simple text up to 160 characters; longer text, lists, links, code blocks, and tables use a popover.

Tooltips open on hover or keyboard focus and can be pinned by click or tap. Popovers open on click or tap. Both dismiss with Escape or outside interaction; a popover also has a close button. Help never submits its enclosing form. Popovers stay inside the viewport and above modal dialogs. Scrolling the surrounding page dismisses them. Without JavaScript or Popover API support, the instructions remain readable inline.

Raw HTML and executable URLs remain text. Root-relative links use the same site; external HTTP(S) links use the renderer’s existing safe link behavior. No HTML injection API is used.

The layout, project, and session shells load `analysis-format.js`, `instructions.js`, and `instructions.css`. No dependency or frontend build is needed.

## Validation

Browser tests use the Playwright installation in `tools/terminal-eval`, or one selected with `PLAYWRIGHT_MODULE`. An optional `CHROMIUM_BIN` selects a system browser.

```sh
node --test internal/web/testdata/instructions.browser.cjs
node --test internal/web/testdata/work_schedule.test.cjs
go test -race ./internal/web
```

## Site audit

| Surface | Simplification |
| --- | --- |
| Shared navigation | One server identity; consistent page names; no per-project server repeats. |
| Projects and Inbox | Direct empty states; Inbox shows reports rather than duplicating all project cards. |
| Goal composer | Goal, collapsed scheduling, and submit action; removed promotional copy, agent counts, and decorative steps. |
| Conversation, workflow, results | Retained actionable status, reviews, requests, output, and evidence; healthy connection timestamps no longer compete with the work. |
| Scheduling and automations | Schedule values stay visible; cron, timezone, and execution guidance use contextual Markdown help. |
| Project setup | Instructions editor first; autonomy and approval configuration in an expandable section. |
| Agent library | Agent descriptions and edit actions; removed revision chatter and explanatory status footer. |
| Plugin list and details | Direct plugin actions; execution instructions in Markdown popovers. |
| Application launch | Removed decorative step numbers; field guidance sits beside labels. |
| Run history and runtime status | Removed duplicate navigation, slogans, decorative labels, and repeated empty-state reassurance. |
| Run, report, and demo history | Kept provenance, output, artifacts, and execution warnings; collapsed metadata and activity; demo explanation in a popover. |
| Job queue and background jobs | Pool policies and lifecycle history collapsed; activation/catalog instructions in popovers. |
| Events, lifecycle operations, autonomy evidence | Kept receipts, status, affected work, and metrics; explanatory guidance available on demand. |
| Native sessions, coordination, and project profiles | Removed duplicate workspace labels and promotional empty state; configuration guidance uses the shared component. |
| Devices, changes, notifications, and directory picker | Kept connection state, comparison source, requests, and folder location; technical details remain expandable. |

Validation uses isolated demo data and local browser checks. No external agent jobs or live project configuration changes are needed for the audit.

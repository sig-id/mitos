# Handheld sound feedback and arrival tracking

Status: Normative subsystem contract. See [specification authority](../README.md)
for precedence and [implementation evidence](../system/IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

Status: device-side effects implemented in esp-notify. No sound files, playback
API or new backend action permissions are needed. The watch generates quiet tap,
completion and notification sounds locally; mute persists on the device.

Current notification behavior uses valid authenticated snapshot responses:
newest pending card identities and pending_total, with a silent initial baseline
and no repeated alert for identical polls, reconnect, revisions or pagination.
An accepted/applied receipt can produce a completion sound. The click or queued
submission alone never means that the answer was accepted/applied.

## Missing: reliable arrivals independently of paginated display state

A paginated snapshot is a frozen view, not an arrival stream. Comparing totals
cannot discover every arrival when other requests complete in the same interval.
Add an optional scoped `attention_epoch` and monotonic `attention_sequence` to
fresh device snapshots (including a lightweight refresh independent of a frozen
page cursor), or an authenticated attention endpoint with equivalent semantics.

- Increment for a newly pending question/approval visible to this paired device;
  do not increment for polls, transcription drafts, revision-only edits, reads,
  receipts or Wi-Fi reconnects. Define reopening as a new attention event.
- Persist sequence/epoch across ordinary Mitos restarts. A reset/restore changes
  the epoch so clients can establish a fresh quiet baseline.
- Return the current counter independently of card pagination, with the existing
  device token scope and tenant boundaries. Never expose another device's events.
- Clients compare epoch/counter, coalesce bursts to one beep, respect local mute
  and microphone use, and never treat sound as approval or application evidence.
- No delivery/read acknowledgement is required to keep the counter current.
  Persisting a client last-seen counter is optional; firmware currently chooses
  a quiet boot baseline rather than replaying existing pending requests.
- Test same-count replacement, net-decreasing concurrent arrivals/completions,
  frozen pagination, reconnect/restart, scoped authorization, and epoch changes.

This counter is specified, not implemented or claimed as part of the current
snapshot wire contract. Existing watch notifications remain best effort until
that backend capability is delivered.

# Upstream source

- Repository: https://github.com/Autumn-27/Norma
- Commit: `4f5684e567607ade605320100ec118056ab38155` (2026-10-18)
- Go version identifier: `v0.4.5-0.20261018113434-4f5684e56760`

Local fixes:
- Capture each tool batch's context to avoid the timeout settlement race.
- Cancel and wait for started tool calls on exit; skip cancelled queued calls.
- Wait for managed processes and PTYs before removing their output directories.
- Preserve executed tool history on stream errors without dispatching partial calls.
- Give settlement tools a live parent context and enforce disabled tools at execution.
- Bound model calls and settlement with separate deadlines.
- Allow bounded consecutive stop-hook corrections and an absolute primary deadline.

Update this record and `go.mod` together when syncing upstream.

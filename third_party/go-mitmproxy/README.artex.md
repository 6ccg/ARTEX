# ARTEX dependency copy

Source: https://github.com/lqqyt2423/go-mitmproxy/tree/v1.9.5
Version: `v1.9.5`, checked against the Go module checksum.

This directory retains the `proxy`, `cert`, and `internal` packages used by ARTEX,
their tests, module files and MIT license. Unused CLI, addons and web UI are omitted.

SOCKS5 failure handling is the unmodified v1.9.5 upstream implementation; the old
ARTEX SOCKS5 patch is no longer maintained separately.

Local changes retained:
- Use the CONNECT target to generate IP certificates when a TLS client omits SNI.
- Drain active requests, HTTP/2 handlers and connections during shutdown, including
  the existing HTTP/2 configuration and connection-close synchronization fixes.

Checks: `go test ./...` from this directory; ARTEX's `internal/traffic` tests cover
HTTPS capture, shutdown and upstream failure behavior.

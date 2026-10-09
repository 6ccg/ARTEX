# ARTEX local patch

Source: https://github.com/lqqyt2423/go-mitmproxy/tree/v1.9.2
Version: `v1.9.2`, verified against the Go module checksum before copying.

This directory retains the `proxy`, `cert`, and `internal` packages used by ARTEX,
their tests, the module files, and the upstream MIT license. The CLI, addons, and
web interface are not used by ARTEX and are omitted.

Patch: a failed SOCKS5 `DialContext` returns a nil connection. Return that error
without calling `Close` on the nil connection. The SOCKS5 dialer already closes
the underlying connection on failure.

Regression check:

```sh
cd third_party/go-mitmproxy
go test ./internal/helper
```

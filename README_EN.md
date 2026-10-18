# Domain Dispatcher

[中文](README.md)

A Go HTTP request router that listens on one or more ports and dispatches
requests by the `Host` header: reverse-proxy (`forward`), HTTP 302 redirect
(`redirect`) or a local canned response (`mock`).

Building, cross-platform releases and Linux/systemd deployment are covered in
[DEPLOY.md](DEPLOY.md).

## Features

- **Domain-based routing**: Match the `Host` header exactly, with `*` as a wildcard fallback
- **Path matching**: Optional `path` filter per rule, exact (`/health`) or prefix wildcard (`/api/*`)
- **Manual rule priority**: `priority` adds to the automatic match score to order overlapping rules
- **Multiple dispatch types**: `forward` (reverse proxy), `redirect` (302), `mock` (local canned response)
- **Header control**: Remove configured headers before forwarding; optionally add `X-Forwarded-*`
- **Forward rewriting**: `forward` rules can override the upstream `Host` header and rewrite the request path with a regular expression
- **SSE-aware forwarding**: Streaming responses are flushed chunk by chunk and the upstream never negotiates compression, so events are delivered immediately instead of in bursts
- **Timeouts**: Upstream dial/header/body-idle/body-duration and server read-header/idle timeouts are all configurable
- **HTTP and HTTPS listeners**: TLS certificates are domain-bound and configured per dispatch rule; the matching certificate is selected by the client's TLS SNI name
- **Source IP blacklist**: Reject individual IPs or CIDR ranges with 403
- **Graceful shutdown**: SIGINT/SIGTERM stops accepting connections and drains in-flight requests within `server.shutdown_timeout`
- **Per-request summary log**: One line for every request (forward, redirect, mock and 404)
- **Optional file logging**: Size-based rotation via `global.log`
- **YAML configuration**: Multiple listeners, each with its own rule set

## Quick Start

```bash
# Build for the current platform
make build

# Create your local config from the tracked template (config.yaml is gitignored)
cp config.example.yaml config.yaml

# Run with the local config (default path when -c is omitted)
./build/domain-dispatcher -c config.yaml
```

Map a test domain to your machine (e.g. in `/etc/hosts`):

```
127.0.0.1 abc-api.com
```

Then send requests through the dispatcher (replace the `-H "Host: ..."` value to
exercise a different rule):

```bash
# forward rule
curl -H "Host: abc-api.com" http://127.0.0.1:8080/hello

# mock rule
curl -H "Host: mock-api.com" http://127.0.0.1:8080/health

# unmatched domain -> 404
curl -H "Host: unknown.com" http://127.0.0.1:8080/
```

## Command-line Options

| Flag | Default | Description |
|------|---------|-------------|
| `-c, --config` | `config.yaml` | Path to the YAML config file (create from `config.example.yaml`) |
| `--version` | - | Print version and build time, then exit |

Exit codes: `0` after a graceful SIGINT/SIGTERM shutdown, `1` when a listener
fails or the configuration is invalid.

## Configuration

See `config.example.yaml` for the fully annotated reference. Every value shown there is
the built-in default, so an omitted key keeps its default. A minimal example:

```yaml
global:
  forward:
    request_timeout: 60s             # max wait for upstream response headers
    stream_idle_timeout: 120s        # abort a body that stalls this long (0s = off)
    response_body_timeout: 300s      # total cap for non-streaming bodies (0s = off)
  server:
    shutdown_timeout: 30s
  log:                               # optional; omit for stderr
    dir: /var/log/domain-dispatcher
    filename: app.log
  add_proxy_headers: true
  source_ip_blacklist:
    - 203.0.113.7
    - 192.168.0.0/16
  debug: false

listeners:
  - port: 8080
    type: http
    dispatch:
      - domain: abc-api.com
        forward_to: https://abc.com/api
      - domain: xyz-api.com
        path: /api/*                 # only requests under /api/
        forward_to: https://xyz.com/backend
      - domain: rewrite-api.com
        forward_to: http://127.0.0.1:9000
        rewrite:
          domain: backend.internal   # overrides the Host header sent upstream
          url:                       # regex substitution on the request path
            pattern: '^/api/v1/(.*)$'
            replacement: '/v2/$1'
      - domain: mock-api.com
        path: /health
        type: mock                   # local response, no upstream
        priority: 100                # raise above the automatic match score
        mock_status: 200
        mock_headers:
          Content-Type: application/json
        mock_body: '{"status":"healthy"}'
      - domain: old-api.com
        forward_to: https://new-api.com
        type: redirect

  - port: 8443
    type: https
    dispatch:
      - domain: abc-api.com
        forward_to: https://abc.com/api
        tls:                         # certificates are configured per rule (https listeners)
          cert_file: /etc/domain-dispatcher/certs/abc-api.com.crt
          key_file: /etc/domain-dispatcher/certs/abc-api.com.key
      - domain: "*"                  # fallback rule; its cert also covers unknown SNI
        forward_to: https://abc.com/api
        tls:
          cert_file: /etc/domain-dispatcher/certs/default.crt
          key_file: /etc/domain-dispatcher/certs/default.key
```

### Configuration Fields

- `global.forward`: Shared forwarding-engine parameters (connection pool,
  dial/TLS timeouts, `request_timeout`, `stream_idle_timeout`,
  `response_body_timeout`, `streaming_flush`, `disable_compression`,
  `force_attempt_http2`, `buffer_size`) — same names and defaults as
  `domain-forwarder`
- `global.server`: HTTP server settings applied to every listener
  (`read_header_timeout`, `idle_timeout`, `max_header_bytes`, `shutdown_timeout`)
- `global.log`: Optional file logging with rotation (`dir`, `filename`,
  `max_size` MB, `max_backups`); logs go to stderr when `dir`/`filename` are empty
- `global.source_ip_blacklist`: Client IPs or CIDR ranges rejected with 403
- `global.add_proxy_headers`: Add `X-Forwarded-For`/`X-Forwarded-Proto`/`X-Forwarded-Host`
  to forwarded requests (default true)
- `global.debug`: Print extra per-request detail logs (the summary line is always printed)
- `listeners`: Array of listeners
  - `port`: Listen port (1-65535)
  - `type`: `http` (default) or `https`
  - `dispatch`: Array of dispatch rules
    - `domain`: Domain to match (without protocol or port); `*` matches any domain
    - `path`: Optional path pattern: exact (`/health`) or prefix wildcard (`/api/*`)
    - `forward_to`: Target URL (required for `forward` and `redirect`)
    - `type`: `forward` (default), `redirect` or `mock`
    - `priority`: Optional integer added to the automatic match score
    - `tls`: Optional certificate configuration (`cert_file`, `key_file`, only
      effective on `https` listeners). TLS certificates are domain-bound, so they
      are configured per rule: during the TLS handshake the certificate of the
      rule matching the client's SNI server name is presented; the `*` rule's
      certificate is the fallback for unknown domains and clients without SNI,
      and without it those connections fail the handshake. An `https` listener
      requires a certificate on at least one rule; startup validation fails on
      missing cert/key files or two different certificates for the same domain
    - `remove_headers`: Headers to strip before forwarding (only `forward`)
    - `rewrite`: Optional forwarding rewrites (only `forward` rules)
      - `domain`: Overrides the `Host` header sent upstream; defaults to the host
        of `forward_to`. This only affects the HTTP `Host` header; TLS SNI and
        certificate verification for HTTPS upstreams still use the `forward_to` host
      - `url`: Regex substitution on the request path (`pattern`, `replacement`);
        the query string is preserved. `replacement` supports capture references
        such as `$1` and `${name}`; `$$` is a literal `$`
    - `mock_status` / `mock_headers` / `mock_body`: Mock response definition (only `mock`)

## How It Works

1. The application starts one HTTP server per configured listener
2. The domain is extracted from the `Host` header (port stripped, IPv6-safe)
3. The listener picks the best matching rule by domain/path score plus the
   optional `priority`
4. `forward` proxies the request to the target URL, `redirect` answers with a
   302, `mock` answers locally
5. Requests whose source IP is blacklisted get a 403; unmatched domains get a 404

Match priority (highest to lowest): exact domain > wildcard domain; exact path >
prefix wildcard path > no path. The rule with the highest combined score wins.

## Request Forwarding Details

- **Method**: Preserved (GET, POST, PUT, DELETE, etc.)
- **Headers**: All headers are copied except hop-by-hop headers and `Host` (set
  to the target host, or to `rewrite.domain` when configured); a `Connection`
  header's listed tokens are stripped too.
  With `disable_compression: true` (default) the client's `Accept-Encoding` is
  not forwarded upstream
- **Path and query**: Original path and query parameters are preserved; when
  `rewrite.url` is configured the path is regex-rewritten first
- **Body**: Request body is forwarded as-is, preserving `Content-Length`
- **Protocol**: Supports forwarding between HTTP and HTTPS
- **Streaming**: `text/event-stream` responses are flushed chunk by chunk
- **Failures**: A forward failure before the response starts returns 502 Bad
  Gateway; failures after the response has started are only logged

## Project Structure

```
domain-dispatcher/
├── main.go                      # Flags, startup, graceful shutdown
├── config.go                    # Configuration types, defaults, loading, validation
├── handler.go                   # Request handling, rule matching, summary logging
├── pkg/forwarder/forwarder.go   # Shared forwarding engine (kept in sync with domain-forwarder)
├── config.example.yaml          # Annotated config template (copy to config.yaml)
├── Makefile                     # Build automation
├── scripts/                     # install.sh / uninstall.sh
├── DEPLOY.md                    # Deployment guide
├── README.md                    # Chinese docs
└── README_EN.md                 # This file
```

## License

MIT

# Domain Dispatcher

A Go application that acts as a domain-based HTTP request dispatcher. It listens on one or more ports and forwards, redirects, or mocks HTTP requests based on the domain and path in the request.

## Features

- **Domain-based routing**: Route requests based on the Host header, with `*` as a fallback wildcard
- **Path matching**: Optional `path` filter per rule, exact (`/health`) or prefix wildcard (`/api/*`)
- **Manual rule priority**: `priority` adds to the automatic match score to order overlapping rules
- **Multiple dispatch types**: `forward` (reverse proxy), `redirect` (302), `mock` (local canned response)
- **Header control**: Remove configured headers before forwarding
- **SSE-aware forwarding**: Streaming responses are flushed chunk by chunk, the client's compression negotiation is not forwarded upstream (prevents event bursts), and response-header / body-idle / body-duration timeouts are configurable
- **HTTP and HTTPS listeners**: HTTPS uses a global certificate configuration
- **Optional file logging**: Size-based rotation via `global.log`
- **YAML configuration**: Multiple listeners, each with its own rule set
- **404 for unmapped domains**: Domains not matching any rule get a 404

## Configuration

See `config.yaml.sample` for the full annotated reference. A minimal example:

```yaml
global:
  forward:
    request_timeout: 60s             # max wait for upstream response headers
    stream_idle_timeout: 120s        # abort a body that stalls this long (0s = off)
    response_body_timeout: 300s      # total cap for non-streaming bodies (0s = off)
  ssl:
    cert_file: /path/to/server.crt
    key_file: /path/to/server.key
  log:                               # optional; omit for stderr
    dir: /var/log/domain-dispatcher
    filename: app.log

listeners:
  - port: 8080
    type: http
    dispatch:
      - domain: abc-api.com
        forward_to: https://abc.com/api
      - domain: xyz-api.com
        path: /api/*                 # only requests under /api/
        forward_to: https://xyz.com/backend
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
```

### Configuration Fields

- `listeners`: Array of listeners
  - `port`: The port number to listen on (1-65535)
  - `type`: `http` (default) or `https` (requires `global.ssl`)
  - `dispatch`: Array of dispatch rules
    - `domain`: The domain to match (without protocol or port); `*` matches any domain
    - `path`: Optional path pattern: exact (`/health`) or prefix wildcard (`/api/*`)
    - `forward_to`: Target URL (required for `forward` and `redirect`, optional for `mock`)
    - `type`: `forward` (default), `redirect` or `mock`
    - `priority`: Optional integer added to the automatic match score
    - `remove_headers`: Headers to strip before forwarding (only `forward`)
    - `mock_status` / `mock_headers` / `mock_body`: Mock response definition (only `mock`)

## Building

```bash
go mod tidy
go build -o domain-dispatcher
```

## Usage

```bash
./domain-dispatcher config.yaml
```

## Example

With the example configuration above:

1. Map `abc.com` to point to your machine (e.g., via `/etc/hosts` or DNS)
2. Start the dispatcher: `./domain-dispatcher config.yaml`
3. Make a request: `curl http://abc.com:8080/hello?name=me`
4. The request gets forwarded to: `https://balala.net/api/hello?name=me`

## How It Works

1. The application listens on each configured port
2. When a request comes in, it extracts the domain from the `Host` header
3. It picks the best matching rule by domain/path score (plus optional `priority`)
4. `forward` proxies the request to the target URL, `redirect` answers with a 302, `mock` answers locally
5. If no rule matches, it returns a 404 Not Found response

## Request Forwarding Details

- **Method**: Preserved (GET, POST, PUT, DELETE, etc.)
- **Headers**: All headers are copied except `Host` (set to target host) and, by default, `Accept-Encoding` (see `disable_compression`)
- **Path and Query**: Original path and query parameters are maintained
- **Body**: Request body is forwarded as-is
- **Protocol**: Supports forwarding between HTTP and HTTPS
- **Streaming**: `text/event-stream` responses are flushed chunk by chunk so SSE events are delivered immediately

## Error Handling

- Invalid configuration: Application exits with error message
- Missing config file: Application exits with usage message
- Network errors before the response starts: Returns 500 Internal Server Error
- Errors after the response has started streaming: Only logged (the status line is already sent)
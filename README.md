# Domain Dispatcher

A Go application that acts as a domain-based HTTP request dispatcher. It listens on a configurable port and forwards requests to different backend services based on the domain in the request.

## Features

- **Domain-based routing**: Route requests based on the Host header
- **Protocol flexibility**: Forward between HTTP and HTTPS protocols
- **Preserves request details**: Maintains method, headers, path, query parameters, and body
- **YAML configuration**: Easy-to-edit configuration file
- **404 for unmapped domains**: Returns 404 for domains not configured in dispatch rules

## Configuration

Create a `config.yaml` file with the following structure:

```yaml
port: 8080
dispatch:
  - domain: abc.com
    forward_to: https://balala.net/api
  - domain: example.org
    forward_to: http://localhost:3000
```

### Configuration Fields

- `port`: The port number to listen on (1-65535)
- `dispatch`: Array of dispatch rules
  - `domain`: The domain to match (without protocol or port)
  - `forward_to`: The full URL to forward requests to (including protocol)

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

1. The application listens on the configured port
2. When a request comes in, it extracts the domain from the `Host` header
3. It looks up the domain in the dispatch configuration
4. If found, it forwards the request to the target URL
5. If not found, it returns a 404 Not Found response
6. All request details (method, headers, path, query, body) are preserved during forwarding

## Request Forwarding Details

- **Method**: Preserved (GET, POST, PUT, DELETE, etc.)
- **Headers**: All headers are copied except `Host` (set to target host)
- **Path and Query**: Original path and query parameters are maintained
- **Body**: Request body is forwarded as-is
- **Protocol**: Supports forwarding between HTTP and HTTPS

## Error Handling

- Invalid configuration: Application exits with error message
- Missing config file: Application exits with usage message
- Network errors during forwarding: Returns 500 Internal Server Error
- Unmapped domains: Returns 404 Not Found
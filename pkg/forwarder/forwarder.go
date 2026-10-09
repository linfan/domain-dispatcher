package forwarder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrResponseStarted wraps failures that occurred after the response had already
// begun reaching the client. Callers must not write an error response in that case.
var ErrResponseStarted = errors.New("response already started")

// Config represents the configuration for forward mode
type Config struct {
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ExpectContinueTimeout time.Duration
	ResponseHeaderTimeout time.Duration
	DialTimeout           time.Duration
	DialKeepAlive         time.Duration
	RequestTimeout        time.Duration // max wait for upstream response headers; does not bound the response body
	StreamingFlush        bool          // flush streaming (SSE) responses chunk by chunk
	ForceAttemptHTTP2     bool
}

// DefaultConfig returns the default forward configuration
func DefaultConfig() Config {
	return Config{
		MaxIdleConns:          1000,
		MaxIdleConnsPerHost:   100,
		MaxConnsPerHost:       100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DialTimeout:           10 * time.Second,
		DialKeepAlive:         30 * time.Second,
		RequestTimeout:        60 * time.Second,
		StreamingFlush:        true,
		ForceAttemptHTTP2:     true,
	}
}

// Forwarder handles HTTP request forwarding with configurable connection pooling
type Forwarder struct {
	config     Config
	httpClient *http.Client
	once       sync.Once
}

// New creates a new Forwarder with the given configuration
func New(config Config) *Forwarder {
	return &Forwarder{
		config: config,
	}
}

// getHTTPClient returns the HTTP client, initializing it on first call
func (f *Forwarder) getHTTPClient() *http.Client {
	f.once.Do(func() {
		transport := &http.Transport{
			MaxIdleConns:          f.config.MaxIdleConns,
			MaxIdleConnsPerHost:   f.config.MaxIdleConnsPerHost,
			MaxConnsPerHost:       f.config.MaxConnsPerHost,
			IdleConnTimeout:       f.config.IdleConnTimeout,
			TLSHandshakeTimeout:   f.config.TLSHandshakeTimeout,
			ExpectContinueTimeout: f.config.ExpectContinueTimeout,
			ResponseHeaderTimeout: f.config.ResponseHeaderTimeout,
			ForceAttemptHTTP2:     f.config.ForceAttemptHTTP2,
			DialContext: (&net.Dialer{
				Timeout:   f.config.DialTimeout,
				KeepAlive: f.config.DialKeepAlive,
			}).DialContext,
		}

		// No Client.Timeout: it also bounds reading the response body, which would
		// cut off long-lived streaming responses. RequestTimeout is applied as a
		// context deadline in ForwardRequest instead.
		f.httpClient = &http.Client{
			Transport: transport,
		}
	})
	return f.httpClient
}

// ForwardRequest forwards an HTTP request to a target URL and writes the response
// to the provided ResponseWriter. It uses an optimized HTTP client with connection
// pooling for high concurrency performance.
// removeHeaders specifies which headers should be removed from the request before forwarding.
// Config.RequestTimeout bounds only the wait for response headers. Errors raised after the
// response has started reaching the client wrap ErrResponseStarted.
func (f *Forwarder) ForwardRequest(w http.ResponseWriter, r *http.Request, targetURLStr string, removeHeaders []string) error {
	// Create new request with the original body
	var bodyReader io.Reader = r.Body
	if r.Body != nil {
		bodyReader = r.Body
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURLStr, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Build a set of headers to remove for efficient lookup
	headersToRemove := make(map[string]struct{}, len(removeHeaders))
	for _, h := range removeHeaders {
		headersToRemove[http.CanonicalHeaderKey(h)] = struct{}{}
	}

	// Copy headers (except Host and headers in removeHeaders)
	req.Header = make(http.Header, len(r.Header))
	for key, values := range r.Header {
		if key == "Host" {
			continue // Don't copy Host header
		}
		if _, shouldRemove := headersToRemove[key]; shouldRemove {
			continue // Skip headers that should be removed
		}
		req.Header[key] = values
	}

	// Parse target URL to get host
	u, err := url.Parse(targetURLStr)
	if err != nil {
		return fmt.Errorf("failed to parse target URL: %w", err)
	}
	req.Host = u.Host

	// RequestTimeout bounds only the wait for the upstream response headers: the
	// deadline is dropped as soon as headers arrive so that streaming response
	// bodies can keep transferring for as long as they need.
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	headerTimer := time.AfterFunc(f.config.RequestTimeout, cancel)
	resp, err := f.getHTTPClient().Do(req)
	headerTimer.Stop()
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Copy response headers efficiently
	for key, values := range resp.Header {
		w.Header()[key] = values
	}

	// Set status code
	w.WriteHeader(resp.StatusCode)

	// Streaming responses are flushed chunk by chunk; everything else keeps the
	// io.Copy fast path (splice/sendfile on Linux when possible).
	streaming := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	if streaming && f.config.StreamingFlush {
		err = copyStreaming(w, resp.Body)
	} else {
		_, err = io.Copy(w, resp.Body)
	}
	if err != nil {
		return fmt.Errorf("%w: failed to copy response body: %w", ErrResponseStarted, err)
	}

	return nil
}

// copyStreaming relays a streaming (SSE) response, flushing after every chunk.
// Without the explicit flush the writes sit in net/http's 2 KiB pre-chunking
// buffer, so events pile up and reach the client in bursts.
func copyStreaming(w http.ResponseWriter, r io.Reader) error {
	rc := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			if err := rc.Flush(); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

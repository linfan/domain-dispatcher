// Package forwarder implements the shared HTTP forwarding engine used by both
// domain-forwarder and domain-dispatcher. Keep this file byte-identical in both
// repositories.
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
	"sync/atomic"
	"time"
)

// ErrResponseStarted wraps failures that occurred after the response had already
// begun reaching the client. Callers must not write an error response in that case.
var ErrResponseStarted = errors.New("response already started")

// ErrStreamIdle marks a response body that was aborted because the upstream sent
// no bytes for Config.StreamIdleTimeout.
var ErrStreamIdle = errors.New("stream idle")

// ErrBodyTimeout marks a non-streaming response body that was aborted because
// transferring it took longer than Config.ResponseBodyTimeout.
var ErrBodyTimeout = errors.New("response body timeout")

// Config represents the configuration of the forwarding engine.
type Config struct {
	BufferSize            int
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ExpectContinueTimeout time.Duration
	ResponseHeaderTimeout time.Duration
	DialTimeout           time.Duration
	DialKeepAlive         time.Duration
	RequestTimeout        time.Duration // max wait for upstream response headers (incl. dial); does not bound the response body; 0 disables it
	StreamIdleTimeout     time.Duration // abort a response body that sends no bytes for this long; 0 disables it
	ResponseBodyTimeout   time.Duration // absolute cap on the non-streaming response body phase; 0 disables it
	StreamingFlush        bool          // flush streaming (SSE) responses chunk by chunk
	StripAcceptEncoding   bool          // drop the client's Accept-Encoding before forwarding (gateways that must not let the upstream compress)
	DisableCompression    bool          // never negotiate compression with the upstream
	ForceAttemptHTTP2     bool
}

// DefaultConfig returns the canonical engine defaults. Both applications
// prepopulate their YAML configuration with the same values.
func DefaultConfig() Config {
	return Config{
		BufferSize:            32 * 1024,
		MaxIdleConns:          1000,
		MaxIdleConnsPerHost:   100,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DialTimeout:           10 * time.Second,
		DialKeepAlive:         30 * time.Second,
		RequestTimeout:        60 * time.Second,
		StreamingFlush:        true,
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
	}
}

// Result captures per-request statistics. Values are populated as far as known
// even when the response has already started; pre-start failures leave the zero
// value.
type Result struct {
	StatusCode int
	ReqSize    int64
	RespSize   int64
	Duration   time.Duration
}

// BufferPool provides reusable copy buffers through a sync.Pool.
type BufferPool struct {
	pool sync.Pool
}

// NewBufferPool creates a buffer pool whose buffers have the given size.
func NewBufferPool(size int) *BufferPool {
	if size <= 0 {
		size = 32 * 1024
	}
	return &BufferPool{
		pool: sync.Pool{
			New: func() any { return make([]byte, size) },
		},
	}
}

// Get returns a buffer from the pool.
func (b *BufferPool) Get() []byte { return b.pool.Get().([]byte) }

// Put returns a buffer to the pool.
func (b *BufferPool) Put(buf []byte) { b.pool.Put(buf) }

// Forwarder handles HTTP request forwarding with configurable connection pooling.
type Forwarder struct {
	config     Config
	buffers    *BufferPool
	httpClient *http.Client
	once       sync.Once
}

// New creates a new Forwarder with the given configuration.
func New(config Config) *Forwarder {
	return &Forwarder{
		config:  config,
		buffers: NewBufferPool(config.BufferSize),
	}
}

// Buffers returns the copy-buffer pool used by the engine so callers (e.g. a
// CONNECT tunnel) can reuse the same buffers.
func (f *Forwarder) Buffers() *BufferPool { return f.buffers }

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
			// A compressing gateway buffers the SSE stream in the compressor,
			// so events pile up there and reach the client in bursts seconds
			// later. Identity responses are valid for every client.
			DisableCompression: f.config.DisableCompression,
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
// Config.RequestTimeout bounds only the wait for response headers; Config.StreamIdleTimeout
// aborts a body that stops delivering bytes, and Config.ResponseBodyTimeout additionally
// caps how long a non-streaming body may take overall. Errors raised after the response
// has started reaching the client wrap ErrResponseStarted.
func (f *Forwarder) ForwardRequest(w http.ResponseWriter, r *http.Request, targetURLStr string, removeHeaders []string) (Result, error) {
	return f.forwardRequest(w, r, targetURLStr, "", removeHeaders)
}

// ForwardRequestWithHost behaves like ForwardRequest but sends hostOverride as
// the Host header when it is non-empty. TLS SNI and certificate verification
// still use the target URL's host.
func (f *Forwarder) ForwardRequestWithHost(w http.ResponseWriter, r *http.Request, targetURLStr, hostOverride string, removeHeaders []string) (Result, error) {
	return f.forwardRequest(w, r, targetURLStr, hostOverride, removeHeaders)
}

func (f *Forwarder) forwardRequest(w http.ResponseWriter, r *http.Request, targetURLStr, hostOverride string, removeHeaders []string) (result Result, err error) {
	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()
	result.ReqSize = r.ContentLength

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURLStr, r.Body)
	if err != nil {
		return result, fmt.Errorf("failed to create request: %w", err)
	}
	// Preserve the incoming Content-Length so the upstream request is not
	// re-framed as chunked (-1 keeps the original unknown/chunked framing).
	req.ContentLength = r.ContentLength

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
		if key == "Accept-Encoding" && f.config.StripAcceptEncoding {
			// Gateways that must not let the upstream compress (a compressing
			// gateway buffers the SSE stream and delivers events in bursts)
			// drop the client's encoding preference. Identity responses are
			// valid for every client.
			continue
		}
		if _, shouldRemove := headersToRemove[key]; shouldRemove {
			continue // Skip headers that should be removed
		}
		req.Header[key] = values
	}
	RemoveHopByHopHeaders(req.Header)

	// Parse target URL to get host
	u, err := url.Parse(targetURLStr)
	if err != nil {
		return result, fmt.Errorf("failed to parse target URL: %w", err)
	}
	req.Host = u.Host
	if hostOverride != "" {
		req.Host = hostOverride
	}

	// RequestTimeout bounds only the wait for the upstream response headers: the
	// deadline is dropped as soon as headers arrive so that streaming response
	// bodies can keep transferring for as long as they need.
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	var abort atomic.Pointer[watchdog]

	var headerTimer *time.Timer
	if f.config.RequestTimeout > 0 {
		headerTimer = time.AfterFunc(f.config.RequestTimeout, cancel)
	}
	resp, err := f.getHTTPClient().Do(req)
	if headerTimer != nil {
		headerTimer.Stop()
	}
	if err != nil {
		return result, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Copy response headers efficiently, dropping hop-by-hop headers first
	RemoveHopByHopHeaders(resp.Header)
	for key, values := range resp.Header {
		w.Header()[key] = values
	}

	// Set status code
	result.StatusCode = resp.StatusCode
	w.WriteHeader(resp.StatusCode)

	streaming := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")

	// Both watchdogs bound the body phase only, and start counting now that the
	// response headers have arrived.
	var body io.Reader = resp.Body
	if f.config.StreamIdleTimeout > 0 {
		idleTimer := armWatchdog(&abort, ErrStreamIdle, f.config.StreamIdleTimeout, cancel)
		defer idleTimer.Stop()
		body = &idleResetReader{r: resp.Body, idle: f.config.StreamIdleTimeout, timer: idleTimer}
	}
	// A streaming body has no meaningful duration cap, so ResponseBodyTimeout only
	// bounds ordinary responses, which are expected to transfer promptly.
	if !streaming && f.config.ResponseBodyTimeout > 0 {
		defer armWatchdog(&abort, ErrBodyTimeout, f.config.ResponseBodyTimeout, cancel).Stop()
	}

	// Streaming responses are flushed chunk by chunk; everything else keeps the
	// sendfile-capable io.Copy fast path (a pooled buffer does not disable it).
	buf := f.buffers.Get()
	defer f.buffers.Put(buf)
	if streaming && f.config.StreamingFlush {
		result.RespSize, err = copyStreaming(w, body, buf)
	} else {
		result.RespSize, err = io.CopyBuffer(w, body, buf)
	}
	if err != nil {
		// The cancellation came from our own watchdog, not from the client hanging up.
		if wd := abort.Load(); wd != nil {
			err = fmt.Errorf("%w after %s: %w", wd.err, wd.limit, err)
		}
		return result, fmt.Errorf("%w: failed to copy response body: %w", ErrResponseStarted, err)
	}

	return result, nil
}

// watchdog records which body-phase limit aborted a response, so the resulting
// error can name it instead of guessing from the remaining context state.
type watchdog struct {
	err   error
	limit time.Duration
}

// armWatchdog starts a timer that publishes reason into abort and then cancels
// the request. The first timer to fire wins.
func armWatchdog(abort *atomic.Pointer[watchdog], reason error, limit time.Duration, cancel context.CancelFunc) *time.Timer {
	wd := &watchdog{err: reason, limit: limit}
	return time.AfterFunc(limit, func() {
		abort.CompareAndSwap(nil, wd)
		cancel()
	})
}

// idleResetReader re-arms the idle timer on every read, so only a genuine
// stall - not a long but healthy stream - trips the timeout.
type idleResetReader struct {
	r     io.Reader
	idle  time.Duration
	timer *time.Timer
}

func (p *idleResetReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 || err == io.EOF {
		p.timer.Reset(p.idle)
	}
	return n, err
}

// copyStreaming relays a streaming (SSE) response, flushing after every chunk.
// Without the explicit flush the writes sit in net/http's 2 KiB pre-chunking
// buffer, so events pile up and reach the client in bursts.
func copyStreaming(w http.ResponseWriter, r io.Reader, buf []byte) (int64, error) {
	rc := http.NewResponseController(w)
	var written int64
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			nw, err := w.Write(buf[:n])
			written += int64(nw)
			if err != nil {
				return written, err
			}
			if err := rc.Flush(); err != nil {
				return written, err
			}
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

// Hop-by-hop headers must not be forwarded by a proxy (RFC 7230, section 6.1).
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
	"Proxy-Connection",
}

// RemoveHopByHopHeaders strips hop-by-hop headers, including the ones listed in
// the Connection header, from h.
func RemoveHopByHopHeaders(h http.Header) {
	if c := h.Get("Connection"); c != "" {
		for _, f := range strings.Split(c, ",") {
			if f = strings.TrimSpace(f); f != "" {
				h.Del(f)
			}
		}
	}
	for _, hdr := range hopByHopHeaders {
		h.Del(hdr)
	}
}

// AddForwardedHeaders appends the client address to X-Forwarded-For and sets
// X-Forwarded-Proto / X-Forwarded-Host when absent. scheme is the scheme the
// client used towards the proxy.
func AddForwardedHeaders(r *http.Request, scheme string) {
	if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if prior := r.Header.Get("X-Forwarded-For"); prior != "" {
			clientIP = prior + ", " + clientIP
		}
		r.Header.Set("X-Forwarded-For", clientIP)
	}
	if r.Header.Get("X-Forwarded-Proto") == "" {
		r.Header.Set("X-Forwarded-Proto", scheme)
	}
	if r.Header.Get("X-Forwarded-Host") == "" {
		r.Header.Set("X-Forwarded-Host", r.Host)
	}
}

// IPBlacklisted reports whether the given IP address is covered by an entry of
// the blacklist. Entries may be individual IPs or CIDR ranges.
func IPBlacklisted(blacklist []string, ipStr string) bool {
	if len(blacklist) == 0 {
		return false
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	for _, blocked := range blacklist {
		if strings.Contains(blocked, "/") {
			if _, ipNet, err := net.ParseCIDR(blocked); err == nil && ipNet.Contains(ip) {
				return true
			}
		} else if blockedIP := net.ParseIP(blocked); blockedIP != nil && blockedIP.Equal(ip) {
			return true
		}
	}
	return false
}

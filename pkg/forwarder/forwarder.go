package forwarder

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

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
	RequestTimeout        time.Duration
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

		f.httpClient = &http.Client{
			Transport: transport,
			Timeout:   f.config.RequestTimeout,
		}
	})
	return f.httpClient
}

// ForwardRequest forwards an HTTP request to a target URL and writes the response
// to the provided ResponseWriter. It uses an optimized HTTP client with connection
// pooling for high concurrency performance.
// removeHeaders specifies which headers should be removed from the request before forwarding.
func (f *Forwarder) ForwardRequest(w http.ResponseWriter, r *http.Request, targetURLStr string, removeHeaders []string) error {
	// Create new request with the original body
	var bodyReader io.Reader = r.Body
	if r.Body != nil {
		bodyReader = r.Body
	}

	req, err := http.NewRequest(r.Method, targetURLStr, bodyReader)
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

	// Send request using the optimized HTTP client with connection pooling
	resp, err := f.getHTTPClient().Do(req)
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

	// Copy response body using io.Copy for efficient streaming
	// This uses splice/sendfile on Linux for zero-copy when possible
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("failed to copy response body: %w", err)
	}

	return nil
}

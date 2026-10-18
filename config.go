package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"domain-dispatcher/pkg/forwarder"

	"gopkg.in/yaml.v3"
)

// RuleType represents the type of dispatch rule
type RuleType string

const (
	RuleTypeForward  RuleType = "forward"
	RuleTypeRedirect RuleType = "redirect"
	RuleTypeMock     RuleType = "mock"
)

// ListenerType represents the protocol type for a listener
type ListenerType string

const (
	ListenerTypeHTTP  ListenerType = "http"
	ListenerTypeHTTPS ListenerType = "https"
)

// TLSConfig represents a TLS certificate bound to one dispatch domain, served
// by https listeners when the client's SNI server name matches that domain.
type TLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// ForwardConfig represents the forwarding engine configuration. The plain
// values are prepopulated with defaults before the YAML file is overlaid, so
// explicitly configured zeros stay zero; the two *bool fields are tri-state
// (unset = default via the accessors).
type ForwardConfig struct {
	BufferSize            int           `yaml:"buffer_size"`
	MaxIdleConns          int           `yaml:"max_idle_conns"`
	MaxIdleConnsPerHost   int           `yaml:"max_idle_conns_per_host"`
	MaxConnsPerHost       int           `yaml:"max_conns_per_host"`
	IdleConnTimeout       time.Duration `yaml:"idle_conn_timeout"`
	TLSHandshakeTimeout   time.Duration `yaml:"tls_handshake_timeout"`
	ExpectContinueTimeout time.Duration `yaml:"expect_continue_timeout"`
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout"`
	DialTimeout           time.Duration `yaml:"dial_timeout"`
	DialKeepAlive         time.Duration `yaml:"dial_keep_alive"`
	RequestTimeout        time.Duration `yaml:"request_timeout"`       // Max wait for upstream response headers; does not limit response body transfer (0 = disabled)
	StreamIdleTimeout     time.Duration `yaml:"stream_idle_timeout"`   // Abort the response body if the upstream sends no bytes for this long (0 = disabled)
	ResponseBodyTimeout   time.Duration `yaml:"response_body_timeout"` // Total duration cap for a non-streaming response body (0 = disabled)
	StreamingFlush        *bool         `yaml:"streaming_flush"`       // Flush streaming (SSE) responses chunk by chunk (default true)
	DisableCompression    *bool         `yaml:"disable_compression"`   // Never negotiate compression with the upstream (default true)
	ForceAttemptHTTP2     bool          `yaml:"force_attempt_http2"`
}

// StreamingFlushEnabled reports whether streaming responses are relayed chunk by
// chunk with an explicit flush after each chunk (default true).
func (fc *ForwardConfig) StreamingFlushEnabled() bool {
	return fc.StreamingFlush == nil || *fc.StreamingFlush
}

// CompressionDisabled reports whether compression negotiation with the upstream
// is disabled (default true). A compressing gateway buffers SSE streams in the
// compressor, so events pile up there and reach the client in bursts.
func (fc *ForwardConfig) CompressionDisabled() bool {
	return fc.DisableCompression == nil || *fc.DisableCompression
}

// Validate checks the forward section for semantic errors.
func (fc *ForwardConfig) Validate() error {
	if fc.BufferSize <= 0 {
		return fmt.Errorf("forward.buffer_size must be positive, got %d", fc.BufferSize)
	}
	if fc.MaxIdleConns < 0 || fc.MaxIdleConnsPerHost < 0 || fc.MaxConnsPerHost < 0 {
		return fmt.Errorf("forward connection pool sizes must not be negative")
	}
	for _, d := range []struct {
		name  string
		value time.Duration
	}{
		{"idle_conn_timeout", fc.IdleConnTimeout},
		{"tls_handshake_timeout", fc.TLSHandshakeTimeout},
		{"expect_continue_timeout", fc.ExpectContinueTimeout},
		{"response_header_timeout", fc.ResponseHeaderTimeout},
		{"dial_timeout", fc.DialTimeout},
		{"dial_keep_alive", fc.DialKeepAlive},
		{"request_timeout", fc.RequestTimeout},
		{"stream_idle_timeout", fc.StreamIdleTimeout},
		{"response_body_timeout", fc.ResponseBodyTimeout},
	} {
		if d.value < 0 {
			return fmt.Errorf("forward.%s must not be negative, got %s", d.name, d.value)
		}
	}
	return nil
}

// EngineConfig converts the YAML forward section into the shared forwarding
// engine configuration. stripAcceptEncoding drops the client's Accept-Encoding
// before forwarding: gateways that must not let the upstream compress set it,
// transparent forward proxies leave it false.
func (fc *ForwardConfig) EngineConfig(stripAcceptEncoding bool) forwarder.Config {
	return forwarder.Config{
		BufferSize:            fc.BufferSize,
		MaxIdleConns:          fc.MaxIdleConns,
		MaxIdleConnsPerHost:   fc.MaxIdleConnsPerHost,
		MaxConnsPerHost:       fc.MaxConnsPerHost,
		IdleConnTimeout:       fc.IdleConnTimeout,
		TLSHandshakeTimeout:   fc.TLSHandshakeTimeout,
		ExpectContinueTimeout: fc.ExpectContinueTimeout,
		ResponseHeaderTimeout: fc.ResponseHeaderTimeout,
		DialTimeout:           fc.DialTimeout,
		DialKeepAlive:         fc.DialKeepAlive,
		RequestTimeout:        fc.RequestTimeout,
		StreamIdleTimeout:     fc.StreamIdleTimeout,
		ResponseBodyTimeout:   fc.ResponseBodyTimeout,
		StreamingFlush:        fc.StreamingFlushEnabled(),
		StripAcceptEncoding:   stripAcceptEncoding,
		DisableCompression:    fc.CompressionDisabled(),
		ForceAttemptHTTP2:     fc.ForceAttemptHTTP2,
	}
}

// ServerConfig represents HTTP server settings applied to every listener.
type ServerConfig struct {
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	MaxHeaderBytes    int           `yaml:"max_header_bytes"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
}

// LogConfig represents the logging configuration.
// When both Dir and Filename are set, all runtime logs are written to the file
// with size-based rotation; otherwise logs go to stderr.
type LogConfig struct {
	Dir        string `yaml:"dir"`         // Directory for log files
	Filename   string `yaml:"filename"`    // Log file name
	MaxSize    int    `yaml:"max_size"`    // Max size in MB of a single log file before rotation
	MaxBackups int    `yaml:"max_backups"` // Max number of rotated log files to keep
}

// Enabled reports whether file logging is enabled
func (lc *LogConfig) Enabled() bool {
	return lc.Dir != "" && lc.Filename != ""
}

// RewriteConfig rewrites a request forwarded by a forward rule.
type RewriteConfig struct {
	Domain string      `yaml:"domain"` // Host header sent upstream; empty keeps the target URL host
	URL    *URLRewrite `yaml:"url"`    // Regex substitution applied to the request path
}

// URLRewrite replaces the request path using a regular expression. The query
// string is not part of the match and is preserved. Replacement supports capture
// references ($1, ${name}); $$ is a literal dollar sign.
type URLRewrite struct {
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`

	re *regexp.Regexp
}

// Apply returns path with the rewrite applied. Config validation compiles the
// pattern at startup.
func (u *URLRewrite) Apply(path string) string {
	return u.re.ReplaceAllString(path, u.Replacement)
}

// DispatchRule represents a single domain dispatch rule
type DispatchRule struct {
	Domain        string            `yaml:"domain"`
	Path          string            `yaml:"path"` // URL path pattern to match (optional, supports wildcard e.g. /api/*)
	ForwardTo     string            `yaml:"forward_to"`
	Type          RuleType          `yaml:"type"`
	Priority      int               `yaml:"priority"`       // Manual priority adjustment added to matching score (default 0)
	TLS           *TLSConfig        `yaml:"tls"`            // TLS certificate for this domain (https listeners only)
	RemoveHeaders []string          `yaml:"remove_headers"` // Headers to remove before forwarding (only for forward type)
	Rewrite       *RewriteConfig    `yaml:"rewrite"`        // Domain and URL rewrites (only for forward type)
	MockStatus    int               `yaml:"mock_status"`    // HTTP status code for mock response (default 200, only for mock type)
	MockHeaders   map[string]string `yaml:"mock_headers"`   // Response headers for mock type
	MockBody      string            `yaml:"mock_body"`      // Response body for mock type
}

// ListenerConfig represents the configuration for a single port listener
type ListenerConfig struct {
	Port     int            `yaml:"port"`
	Type     ListenerType   `yaml:"type"` // Protocol type: http (default) or https
	Dispatch []DispatchRule `yaml:"dispatch"`
}

// GlobalConfig represents the global application configuration
type GlobalConfig struct {
	Forward           ForwardConfig `yaml:"forward"`
	Server            ServerConfig  `yaml:"server"`
	Log               LogConfig     `yaml:"log"`
	SourceIPBlacklist []string      `yaml:"source_ip_blacklist"`
	AddProxyHeaders   bool          `yaml:"add_proxy_headers"` // Add X-Forwarded-For/Proto/Host when forwarding
	Debug             bool          `yaml:"debug"`
}

// Config represents the application configuration
type Config struct {
	Global    GlobalConfig     `yaml:"global"`
	Listeners []ListenerConfig `yaml:"listeners"`
}

// defaultForwardConfig returns the built-in forward section defaults. The
// values mirror pkg/forwarder.DefaultConfig; keep the two in sync.
func defaultForwardConfig() ForwardConfig {
	return ForwardConfig{
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
		ForceAttemptHTTP2:     true,
	}
}

// defaultConfig returns the full built-in defaults; the YAML file is overlaid
// on top, so values omitted there keep these defaults.
func defaultConfig() *Config {
	return &Config{
		Global: GlobalConfig{
			Forward: defaultForwardConfig(),
			Server: ServerConfig{
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       120 * time.Second,
				MaxHeaderBytes:    1 << 20,
				ShutdownTimeout:   30 * time.Second,
			},
			Log:             LogConfig{MaxSize: 100, MaxBackups: 10},
			AddProxyHeaders: true,
		},
	}
}

// LoadConfig loads configuration from a YAML file, overlaying it on the built-in
// defaults and validating the result.
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalizeHostname lowercases a DNS name and drops its optional trailing dot;
// DNS names are case-insensitive, so Host, SNI and configured domains must be
// compared in the same normalized form.
func normalizeHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// normalize fills per-slice defaults that whole-struct defaults cannot express.
func normalize(cfg *Config) {
	for i := range cfg.Listeners {
		listener := &cfg.Listeners[i]
		if listener.Type == "" {
			listener.Type = ListenerTypeHTTP
		}
		for j := range listener.Dispatch {
			rule := &listener.Dispatch[j]
			rule.Domain = normalizeHostname(rule.Domain)
			if rule.Type == "" {
				rule.Type = RuleTypeForward
			}
			if rule.Type == RuleTypeMock && rule.MockStatus == 0 {
				rule.MockStatus = http.StatusOK
			}
		}
	}
}

// Validate checks the configuration for semantic errors.
func (cfg *Config) Validate() error {
	if len(cfg.Listeners) == 0 {
		return fmt.Errorf("no listeners configured")
	}

	if err := cfg.Global.Forward.Validate(); err != nil {
		return err
	}

	if cfg.Global.Server.MaxHeaderBytes <= 0 {
		return fmt.Errorf("global.server.max_header_bytes must be positive, got %d", cfg.Global.Server.MaxHeaderBytes)
	}
	if cfg.Global.Server.ReadHeaderTimeout < 0 || cfg.Global.Server.IdleTimeout < 0 || cfg.Global.Server.ShutdownTimeout < 0 {
		return fmt.Errorf("global.server timeouts must not be negative")
	}

	for _, entry := range cfg.Global.SourceIPBlacklist {
		if err := validateBlacklistEntry(entry); err != nil {
			return fmt.Errorf("global.source_ip_blacklist: %w", err)
		}
	}

	for i := range cfg.Listeners {
		listener := &cfg.Listeners[i]
		if listener.Port <= 0 || listener.Port > 65535 {
			return fmt.Errorf("listener %d: invalid port: %d (must be between 1 and 65535)", i+1, listener.Port)
		}

		if listener.Type != ListenerTypeHTTP && listener.Type != ListenerTypeHTTPS {
			return fmt.Errorf("listener %d: invalid type: %s (must be 'http' or 'https')", i+1, listener.Type)
		}

		// TLS certificates are domain-bound, so https listeners serve one
		// certificate per dispatch rule, selected by the client's SNI name.
		if listener.Type == ListenerTypeHTTPS {
			hasCert := false
			certFileByDomain := make(map[string]string)
			keyFileByDomain := make(map[string]string)
			for j, rule := range listener.Dispatch {
				if rule.TLS == nil {
					continue
				}
				if rule.TLS.CertFile == "" || rule.TLS.KeyFile == "" {
					return fmt.Errorf("listener %d, rule %d: tls.cert_file and tls.key_file are both required", i+1, j+1)
				}
				if _, err := os.Stat(rule.TLS.CertFile); os.IsNotExist(err) {
					return fmt.Errorf("listener %d, rule %d: tls cert file not found: %s", i+1, j+1, rule.TLS.CertFile)
				}
				if _, err := os.Stat(rule.TLS.KeyFile); os.IsNotExist(err) {
					return fmt.Errorf("listener %d, rule %d: tls key file not found: %s", i+1, j+1, rule.TLS.KeyFile)
				}
				if prev, ok := certFileByDomain[rule.Domain]; ok && (prev != rule.TLS.CertFile || keyFileByDomain[rule.Domain] != rule.TLS.KeyFile) {
					return fmt.Errorf("listener %d: dispatch rules for domain %q configure conflicting tls certificates (cert %s key %s vs cert %s key %s)",
						i+1, rule.Domain, prev, keyFileByDomain[rule.Domain], rule.TLS.CertFile, rule.TLS.KeyFile)
				}
				certFileByDomain[rule.Domain] = rule.TLS.CertFile
				keyFileByDomain[rule.Domain] = rule.TLS.KeyFile
				hasCert = true
			}
			if !hasCert {
				return fmt.Errorf("listener %d: https listener requires a tls certificate on at least one dispatch rule", i+1)
			}
		} else {
			for j, rule := range listener.Dispatch {
				if rule.TLS != nil {
					return fmt.Errorf("listener %d, rule %d: tls certificate configured but listener type is not https", i+1, j+1)
				}
			}
		}

		if len(listener.Dispatch) == 0 {
			return fmt.Errorf("listener %d: no dispatch rules configured", i+1)
		}

		for j := range listener.Dispatch {
			rule := &listener.Dispatch[j]
			if rule.Type != RuleTypeForward && rule.Type != RuleTypeRedirect && rule.Type != RuleTypeMock {
				return fmt.Errorf("listener %d, rule %d: invalid type: %s (must be 'forward', 'redirect' or 'mock')", i+1, j+1, rule.Type)
			}
			// forward_to is not required for mock rules
			if rule.Type != RuleTypeMock && rule.ForwardTo == "" {
				return fmt.Errorf("listener %d, rule %d: forward_to cannot be empty", i+1, j+1)
			}
			if rule.Rewrite == nil {
				continue
			}
			if rule.Type != RuleTypeForward {
				return fmt.Errorf("listener %d, rule %d: rewrite is only supported for forward rules", i+1, j+1)
			}
			if rule.Rewrite.URL == nil {
				continue
			}
			if rule.Rewrite.URL.Pattern == "" {
				return fmt.Errorf("listener %d, rule %d: rewrite.url.pattern cannot be empty", i+1, j+1)
			}
			re, err := regexp.Compile(rule.Rewrite.URL.Pattern)
			if err != nil {
				return fmt.Errorf("listener %d, rule %d: invalid rewrite.url.pattern: %w", i+1, j+1, err)
			}
			rule.Rewrite.URL.re = re
		}
	}

	return nil
}

// validateBlacklistEntry checks a single source_ip_blacklist entry, which may be
// an individual IP or a CIDR range.
func validateBlacklistEntry(entry string) error {
	if strings.Contains(entry, "/") {
		if _, _, err := net.ParseCIDR(entry); err != nil {
			return fmt.Errorf("invalid CIDR range %q", entry)
		}
		return nil
	}
	if net.ParseIP(entry) == nil {
		return fmt.Errorf("invalid IP address %q", entry)
	}
	return nil
}

// FindRule finds the best matching dispatch rule for a given domain and request path.
// Domain matching is case-insensitive and ignores a trailing dot.
// Priority (highest to lowest): exact domain > wildcard domain; exact path > wildcard path > no path.
// The rule with the highest combined score wins. When matching prefix wildcard paths tie,
// the longest prefix wins; remaining ties go to the first configured rule. Each rule's
// Priority field is added to its score, allowing manual override of the automatic order.
func (lc *ListenerConfig) FindRule(domain, reqPath string) (DispatchRule, bool) {
	domain = normalizeHostname(domain)

	var bestRule *DispatchRule
	bestScore := -1
	bestWildcardLen := -1
	bestIsWildcard := false

	for i := range lc.Dispatch {
		rule := &lc.Dispatch[i]
		ruleDomain := normalizeHostname(rule.Domain)
		score := rule.Priority
		isWildcard := false
		wildcardLen := -1

		if ruleDomain == domain {
			score += 100
		} else if ruleDomain == "*" {
			score += 10
		} else {
			continue
		}

		if rule.Path != "" {
			if rule.Path == reqPath {
				score += 50
			} else if strings.HasSuffix(rule.Path, "/*") {
				prefix := rule.Path[:len(rule.Path)-1]
				if !strings.HasPrefix(reqPath, prefix) {
					continue
				}
				score += 5
				isWildcard = true
				wildcardLen = len(prefix)
			} else {
				continue
			}
		}

		if score > bestScore || (score == bestScore && isWildcard && bestIsWildcard && wildcardLen > bestWildcardLen) {
			bestScore = score
			bestWildcardLen = wildcardLen
			bestIsWildcard = isWildcard
			bestRule = rule
		}
	}

	if bestRule != nil {
		return *bestRule, true
	}
	return DispatchRule{}, false
}

package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
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

// TransportConfig represents the upstream transport configuration used by the
// forwarding engine. The plain values are prepopulated with defaults before the
// YAML file is overlaid, so explicitly configured zeros stay zero; the two
// *bool fields are tri-state (unset = default via the accessors).
type TransportConfig struct {
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
func (fc *TransportConfig) StreamingFlushEnabled() bool {
	return fc.StreamingFlush == nil || *fc.StreamingFlush
}

// CompressionDisabled reports whether compression negotiation with the upstream
// is disabled (default true). A compressing gateway buffers SSE streams in the
// compressor, so events pile up there and reach the client in bursts.
func (fc *TransportConfig) CompressionDisabled() bool {
	return fc.DisableCompression == nil || *fc.DisableCompression
}

// Validate checks the transport section for semantic errors.
func (fc *TransportConfig) Validate() error {
	if fc.BufferSize <= 0 {
		return fmt.Errorf("transport.buffer_size must be positive, got %d", fc.BufferSize)
	}
	if fc.MaxIdleConns < 0 || fc.MaxIdleConnsPerHost < 0 || fc.MaxConnsPerHost < 0 {
		return fmt.Errorf("transport connection pool sizes must not be negative")
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
			return fmt.Errorf("transport.%s must not be negative, got %s", d.name, d.value)
		}
	}
	return nil
}

// EngineConfig converts the YAML transport section into the shared forwarding
// engine configuration. stripAcceptEncoding drops the client's Accept-Encoding
// before forwarding: gateways that must not let the upstream compress set it,
// transparent forward proxies leave it false.
func (fc *TransportConfig) EngineConfig(stripAcceptEncoding bool) forwarder.Config {
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

// EditConfig configures request edits applied before a forward rule's request
// is sent upstream. AddHeaders sets (overwrites) headers on every forwarded
// request after add_proxy_headers has run.
type EditConfig struct {
	Enabled    bool              `yaml:"enabled"`
	AddHeaders map[string]string `yaml:"add_headers"`
}

// MirrorConfig configures best-effort traffic mirroring for a forward rule:
// every forwarded request is also delivered, asynchronously, to Target.
// Delivery failures and queue overflow never affect the original request.
type MirrorConfig struct {
	Enabled      bool          `yaml:"enabled"`
	Target       string        `yaml:"target"`         // Absolute http(s) base URL; the forwarded path and query are appended
	Timeout      time.Duration `yaml:"timeout"`        // Per-delivery timeout (default 5s)
	MaxBodyBytes int64         `yaml:"max_body_bytes"` // Max request body captured; larger bodies are not mirrored (default 1MiB)
	QueueSize    int           `yaml:"queue_size"`     // Pending mirror jobs; overflow is dropped (default 256)
}

// Validate checks the mirror section for semantic errors.
func (mc *MirrorConfig) Validate() error {
	if !mc.Enabled {
		return nil
	}
	if mc.Target == "" {
		return fmt.Errorf("mirror.target is required when mirror is enabled")
	}
	u, err := url.Parse(mc.Target)
	if err != nil {
		return fmt.Errorf("mirror.target: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("mirror.target %q must use http or https", mc.Target)
	}
	if u.Host == "" {
		return fmt.Errorf("mirror.target %q has no host", mc.Target)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("mirror.target %q must not contain a query or fragment", mc.Target)
	}
	if mc.Timeout <= 0 {
		return fmt.Errorf("mirror.timeout must be positive, got %s", mc.Timeout)
	}
	if mc.MaxBodyBytes < 0 {
		return fmt.Errorf("mirror.max_body_bytes must not be negative, got %d", mc.MaxBodyBytes)
	}
	if mc.QueueSize <= 0 {
		return fmt.Errorf("mirror.queue_size must be positive, got %d", mc.QueueSize)
	}
	return nil
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
	Edit          *EditConfig       `yaml:"edit"`           // Request header edits (only for forward type)
	Mirror        *MirrorConfig     `yaml:"mirror"`         // Async traffic mirroring (only for forward type)
	MockStatus    int               `yaml:"mock_status"`    // HTTP status code for mock response (default 200, only for mock type)
	MockHeaders   map[string]string `yaml:"mock_headers"`   // Response headers for mock type
	MockBody      string            `yaml:"mock_body"`      // Response body for mock type

	mirror *forwarder.Mirror // Runtime mirror queue, built by PrepareRuntime
}

// ListenerConfig represents the configuration for a single port listener
type ListenerConfig struct {
	Port     int            `yaml:"port"`
	Type     ListenerType   `yaml:"type"` // Protocol type: http (default) or https
	Dispatch []DispatchRule `yaml:"dispatch"`
}

// AccessControlConfig represents request admission rules shared by all
// listeners.
type AccessControlConfig struct {
	SourceIP SourceIPAccessConfig `yaml:"source_ip"`
}

// SourceIPAccessConfig filters requests by client IP. Entries may be
// individual IPs or CIDR ranges.
type SourceIPAccessConfig struct {
	Blacklist []string `yaml:"blacklist"`
	Whitelist []string `yaml:"whitelist"`
}

// Denied reports whether clientIP is rejected by the access rules. A whitelist
// match always admits; a non-empty whitelist rejects every other address, so it
// takes precedence over the blacklist. With an empty whitelist only blacklisted
// addresses are rejected.
func (c *SourceIPAccessConfig) Denied(clientIP string) bool {
	if len(c.Whitelist) > 0 {
		return !forwarder.IPListed(c.Whitelist, clientIP)
	}
	return forwarder.IPListed(c.Blacklist, clientIP)
}

// GlobalConfig represents the settings shared by all listeners. Its keys are
// inlined at the top level of the configuration file.
type GlobalConfig struct {
	Transport       TransportConfig     `yaml:"transport"`
	Server          ServerConfig        `yaml:"server"`
	Log             LogConfig           `yaml:"log"`
	AccessControl   AccessControlConfig `yaml:"access_control"`
	AddProxyHeaders bool                `yaml:"add_proxy_headers"` // Add X-Forwarded-For/Proto/Host when forwarding
	Debug           bool                `yaml:"debug"`
}

// Config represents the application configuration. The global settings are
// inlined at the top level; only listeners is nested.
type Config struct {
	GlobalConfig `yaml:",inline"`
	Listeners    []ListenerConfig `yaml:"listeners"`
}

// defaultTransportConfig returns the built-in transport section defaults,
// derived from pkg/forwarder.DefaultConfig so the engine and the YAML defaults
// cannot drift apart.
func defaultTransportConfig() TransportConfig {
	d := forwarder.DefaultConfig()
	return TransportConfig{
		BufferSize:            d.BufferSize,
		MaxIdleConns:          d.MaxIdleConns,
		MaxIdleConnsPerHost:   d.MaxIdleConnsPerHost,
		MaxConnsPerHost:       d.MaxConnsPerHost,
		IdleConnTimeout:       d.IdleConnTimeout,
		TLSHandshakeTimeout:   d.TLSHandshakeTimeout,
		ExpectContinueTimeout: d.ExpectContinueTimeout,
		ResponseHeaderTimeout: d.ResponseHeaderTimeout,
		DialTimeout:           d.DialTimeout,
		DialKeepAlive:         d.DialKeepAlive,
		RequestTimeout:        d.RequestTimeout,
		StreamIdleTimeout:     d.StreamIdleTimeout,
		ResponseBodyTimeout:   d.ResponseBodyTimeout,
		StreamingFlush:        &d.StreamingFlush,
		DisableCompression:    &d.DisableCompression,
		ForceAttemptHTTP2:     d.ForceAttemptHTTP2,
	}
}

// defaultConfig returns the full built-in defaults; the YAML file is overlaid
// on top, so values omitted there keep these defaults.
func defaultConfig() *Config {
	return &Config{
		GlobalConfig: GlobalConfig{
			Transport: defaultTransportConfig(),
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

	// The pre-restructure layout nested these keys under "global". Unknown keys
	// are ignored by the YAML decoder, so reject the old layout explicitly
	// instead of silently running with defaults (e.g. an empty IP blacklist).
	var legacy map[string]interface{}
	if err := yaml.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	if _, ok := legacy["global"]; ok {
		return nil, fmt.Errorf(`config: the "global" section was removed; move its keys to the top level (global.forward is now transport)`)
	}
	if _, ok := legacy["source_ip_blacklist"]; ok {
		return nil, fmt.Errorf(`config: "source_ip_blacklist" was moved to "access_control.source_ip.blacklist"`)
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
			if rule.Mirror != nil && rule.Mirror.Enabled {
				// Per-rule slices cannot be prepopulated before the YAML is
				// overlaid, so omitted fields get their defaults here. Explicit
				// zeros are indistinguishable from omitted ones.
				if rule.Mirror.Timeout == 0 {
					rule.Mirror.Timeout = 5 * time.Second
				}
				if rule.Mirror.MaxBodyBytes == 0 {
					rule.Mirror.MaxBodyBytes = 1 << 20
				}
				if rule.Mirror.QueueSize == 0 {
					rule.Mirror.QueueSize = 256
				}
			}
		}
	}
}

// PrepareRuntime builds the per-rule runtime state that cannot come from YAML:
// mirror delivery queues are created here so their workers start once at
// startup rather than per request. Call it after LoadConfig and before serving.
func (cfg *Config) PrepareRuntime() {
	for i := range cfg.Listeners {
		for j := range cfg.Listeners[i].Dispatch {
			rule := &cfg.Listeners[i].Dispatch[j]
			if rule.Mirror == nil || !rule.Mirror.Enabled {
				continue
			}
			target, err := url.Parse(rule.Mirror.Target)
			if err != nil {
				continue // Validate reports this before startup
			}
			rule.mirror = forwarder.NewMirror(forwarder.MirrorConfig{
				Target:       target,
				Timeout:      rule.Mirror.Timeout,
				MaxBodyBytes: rule.Mirror.MaxBodyBytes,
				QueueSize:    rule.Mirror.QueueSize,
				Debug:        cfg.Debug,
			})
		}
	}
}

// Validate checks the configuration for semantic errors.
func (cfg *Config) Validate() error {
	if len(cfg.Listeners) == 0 {
		return fmt.Errorf("no listeners configured")
	}

	if err := cfg.Transport.Validate(); err != nil {
		return err
	}

	if cfg.Server.MaxHeaderBytes <= 0 {
		return fmt.Errorf("server.max_header_bytes must be positive, got %d", cfg.Server.MaxHeaderBytes)
	}
	if cfg.Server.ReadHeaderTimeout < 0 || cfg.Server.IdleTimeout < 0 || cfg.Server.ShutdownTimeout < 0 {
		return fmt.Errorf("server timeouts must not be negative")
	}

	for _, list := range []struct {
		name    string
		entries []string
	}{
		{"access_control.source_ip.blacklist", cfg.AccessControl.SourceIP.Blacklist},
		{"access_control.source_ip.whitelist", cfg.AccessControl.SourceIP.Whitelist},
	} {
		for _, entry := range list.entries {
			if err := validateIPEntry(entry); err != nil {
				return fmt.Errorf("%s: %w", list.name, err)
			}
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
			if rule.Type != RuleTypeForward {
				if rule.Edit != nil {
					return fmt.Errorf("listener %d, rule %d: edit is only supported for forward rules", i+1, j+1)
				}
				if rule.Mirror != nil {
					return fmt.Errorf("listener %d, rule %d: mirror is only supported for forward rules", i+1, j+1)
				}
			}
			if rule.Edit != nil && rule.Edit.Enabled {
				for name, value := range rule.Edit.AddHeaders {
					if !validHeaderName(name) {
						return fmt.Errorf("listener %d, rule %d: edit.add_headers: invalid header name %q", i+1, j+1, name)
					}
					if strings.ContainsAny(value, "\r\n\x00") {
						return fmt.Errorf("listener %d, rule %d: edit.add_headers.%s: value must not contain CR, LF or NUL", i+1, j+1, name)
					}
				}
			}
			if rule.Mirror != nil {
				if err := rule.Mirror.Validate(); err != nil {
					return fmt.Errorf("listener %d, rule %d: %w", i+1, j+1, err)
				}
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

// validHeaderName reports whether name is an RFC 7230 token, the character set
// HTTP header field names are restricted to.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validateIPEntry checks a single blacklist or whitelist entry, which may be an
// individual IP or a CIDR range.
func validateIPEntry(entry string) error {
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

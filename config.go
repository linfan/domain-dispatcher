package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

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

// SSLConfig represents the global SSL/TLS configuration
type SSLConfig struct {
	CertFile string `yaml:"cert_file"` // Path to SSL certificate file
	KeyFile  string `yaml:"key_file"`  // Path to SSL private key file
}

// ForwardConfig represents the configuration for forward mode with default values
type ForwardConfig struct {
	MaxIdleConns          int           `yaml:"max_idle_conns"`
	MaxIdleConnsPerHost   int           `yaml:"max_idle_conns_per_host"`
	MaxConnsPerHost       int           `yaml:"max_conns_per_host"`
	IdleConnTimeout       time.Duration `yaml:"idle_conn_timeout"`
	TLSHandshakeTimeout   time.Duration `yaml:"tls_handshake_timeout"`
	ExpectContinueTimeout time.Duration `yaml:"expect_continue_timeout"`
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout"`
	DialTimeout           time.Duration `yaml:"dial_timeout"`
	DialKeepAlive         time.Duration `yaml:"dial_keep_alive"`
	RequestTimeout        time.Duration `yaml:"request_timeout"`   // Max wait for upstream response headers; does not limit response body transfer
	StreamingFlush        *bool         `yaml:"streaming_flush"`   // Flush streaming (SSE) responses chunk by chunk (default true)
	ForceAttemptHTTP2     bool          `yaml:"force_attempt_http2"`
}

// StreamingFlushEnabled reports whether streaming responses are relayed chunk by
// chunk with an explicit flush after each chunk (default true). When disabled,
// io.Copy's fast path is used for streaming responses as well.
func (fc *ForwardConfig) StreamingFlushEnabled() bool {
	return fc.StreamingFlush == nil || *fc.StreamingFlush
}

// SetDefaults sets default values for forward configuration
func (fc *ForwardConfig) SetDefaults() {
	if fc.MaxIdleConns <= 0 {
		fc.MaxIdleConns = 1000
	}
	if fc.MaxIdleConnsPerHost <= 0 {
		fc.MaxIdleConnsPerHost = 100
	}
	if fc.MaxConnsPerHost <= 0 {
		fc.MaxConnsPerHost = 100
	}
	if fc.IdleConnTimeout <= 0 {
		fc.IdleConnTimeout = 90 * time.Second
	}
	if fc.TLSHandshakeTimeout <= 0 {
		fc.TLSHandshakeTimeout = 10 * time.Second
	}
	if fc.ExpectContinueTimeout <= 0 {
		fc.ExpectContinueTimeout = 1 * time.Second
	}
	if fc.ResponseHeaderTimeout <= 0 {
		fc.ResponseHeaderTimeout = 30 * time.Second
	}
	if fc.DialTimeout <= 0 {
		fc.DialTimeout = 10 * time.Second
	}
	if fc.DialKeepAlive <= 0 {
		fc.DialKeepAlive = 30 * time.Second
	}
	if fc.RequestTimeout <= 0 {
		fc.RequestTimeout = 60 * time.Second
	}
	// ForceAttemptHTTP2 defaults to true
}

// DispatchRule represents a single domain dispatch rule
type DispatchRule struct {
	Domain        string            `yaml:"domain"`
	Path          string            `yaml:"path"` // URL path pattern to match (optional, supports wildcard e.g. /api/*)
	ForwardTo     string            `yaml:"forward_to"`
	Type          RuleType          `yaml:"type"`
	Priority      int               `yaml:"priority"`       // Manual priority adjustment added to matching score (default 0)
	RemoveHeaders []string          `yaml:"remove_headers"` // Headers to remove before forwarding (only for forward type)
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

// LogConfig represents the logging configuration.
// When both Dir and Filename are set, all runtime logs are written to the file
// with size-based rotation.
type LogConfig struct {
	Dir        string `yaml:"dir"`         // Directory for log files
	Filename   string `yaml:"filename"`    // Log file name
	MaxSize    int    `yaml:"max_size"`    // Max size in MB of a single log file before rotation (default 100)
	MaxBackups int    `yaml:"max_backups"` // Max number of rotated log files to keep (default 10)
}

// Enabled reports whether file logging is enabled
func (lc *LogConfig) Enabled() bool {
	return lc.Dir != "" && lc.Filename != ""
}

// SetDefaults sets default values for log configuration
func (lc *LogConfig) SetDefaults() {
	if lc.MaxSize <= 0 {
		lc.MaxSize = 100
	}
	if lc.MaxBackups <= 0 {
		lc.MaxBackups = 10
	}
}

// GlobalConfig represents the global application configuration
type GlobalConfig struct {
	Forward ForwardConfig `yaml:"forward"`
	SSL     SSLConfig     `yaml:"ssl"` // Global SSL/TLS configuration
	Log     LogConfig     `yaml:"log"` // Logging configuration
}

// Config represents the application configuration
type Config struct {
	Global    GlobalConfig     `yaml:"global"`
	Listeners []ListenerConfig `yaml:"listeners"`
}

// LoadConfig loads configuration from a YAML file
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set defaults for forward configuration
	config.Global.Forward.SetDefaults()

	// Set defaults for log configuration
	config.Global.Log.SetDefaults()

	// Validate configuration
	if len(config.Listeners) == 0 {
		return nil, fmt.Errorf("no listeners configured")
	}

	for i, listener := range config.Listeners {
		if listener.Port <= 0 || listener.Port > 65535 {
			return nil, fmt.Errorf("listener %d: invalid port: %d (must be between 1 and 65535)", i+1, listener.Port)
		}

		// Default to http if type is not set or empty
		if listener.Type == "" {
			config.Listeners[i].Type = ListenerTypeHTTP
		} else if listener.Type != ListenerTypeHTTP && listener.Type != ListenerTypeHTTPS {
			return nil, fmt.Errorf("listener %d: invalid type: %s (must be 'http' or 'https')", i+1, listener.Type)
		}

		// Validate SSL configuration for https listeners
		if listener.Type == ListenerTypeHTTPS {
			if config.Global.SSL.CertFile == "" {
				return nil, fmt.Errorf("listener %d: ssl.cert_file is required for https listener", i+1)
			}
			if config.Global.SSL.KeyFile == "" {
				return nil, fmt.Errorf("listener %d: ssl.key_file is required for https listener", i+1)
			}
			// Check if cert file exists
			if _, err := os.Stat(config.Global.SSL.CertFile); os.IsNotExist(err) {
				return nil, fmt.Errorf("listener %d: ssl cert file not found: %s", i+1, config.Global.SSL.CertFile)
			}
			// Check if key file exists
			if _, err := os.Stat(config.Global.SSL.KeyFile); os.IsNotExist(err) {
				return nil, fmt.Errorf("listener %d: ssl key file not found: %s", i+1, config.Global.SSL.KeyFile)
			}
		}

		if len(listener.Dispatch) == 0 {
			return nil, fmt.Errorf("listener %d: no dispatch rules configured", i+1)
		}

		for j, rule := range listener.Dispatch {
			// Domain can be "*" to match all domains (including empty Host header)
			// Default to forward if type is not set or empty
			if rule.Type == "" {
				config.Listeners[i].Dispatch[j].Type = RuleTypeForward
			} else if rule.Type != RuleTypeForward && rule.Type != RuleTypeRedirect && rule.Type != RuleTypeMock {
				return nil, fmt.Errorf("listener %d, rule %d: invalid type: %s (must be 'forward', 'redirect' or 'mock')", i+1, j+1, rule.Type)
			}

			// forward_to is not required for mock rules
			if rule.Type != RuleTypeMock && rule.ForwardTo == "" {
				return nil, fmt.Errorf("listener %d, rule %d: forward_to cannot be empty", i+1, j+1)
			}

			if rule.Type == RuleTypeMock {
				if config.Listeners[i].Dispatch[j].MockStatus == 0 {
					config.Listeners[i].Dispatch[j].MockStatus = http.StatusOK
				}
			}
		}
	}

	return &config, nil
}

// FindRule finds the best matching dispatch rule for a given domain and request path.
// Priority (highest to lowest): exact domain > wildcard domain; exact path > wildcard path > no path.
// The rule with the highest combined score wins. Each rule's Priority field is added to its score,
// allowing manual override of the automatic matching order.
func (lc *ListenerConfig) FindRule(domain, reqPath string) (DispatchRule, bool) {
	var bestRule *DispatchRule
	bestScore := -1

	for i := range lc.Dispatch {
		rule := &lc.Dispatch[i]
		score := rule.Priority

		if rule.Domain == domain {
			score += 100
		} else if rule.Domain == "*" {
			score += 10
		} else {
			continue
		}

		if rule.Path != "" {
			if rule.Path == reqPath {
				score += 50
			} else if strings.HasSuffix(rule.Path, "/*") && strings.HasPrefix(reqPath, rule.Path[:len(rule.Path)-1]) {
				score += 5
			} else {
				continue
			}
		}

		if score > bestScore {
			bestScore = score
			bestRule = rule
		}
	}

	if bestRule != nil {
		return *bestRule, true
	}
	return DispatchRule{}, false
}

package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// RuleType represents the type of dispatch rule
type RuleType string

const (
	RuleTypeForward  RuleType = "forward"
	RuleTypeRedirect RuleType = "redirect"
)

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
	RequestTimeout        time.Duration `yaml:"request_timeout"`
	ForceAttemptHTTP2     bool          `yaml:"force_attempt_http2"`
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

// DispatchRule represents a single domain forwarding or redirect rule
type DispatchRule struct {
	Domain    string   `yaml:"domain"`
	ForwardTo string   `yaml:"forward_to"`
	Type      RuleType `yaml:"type"`
}

// ListenerConfig represents the configuration for a single port listener
type ListenerConfig struct {
	Port     int            `yaml:"port"`
	Dispatch []DispatchRule `yaml:"dispatch"`
}

// GlobalConfig represents the global application configuration
type GlobalConfig struct {
	Forward ForwardConfig `yaml:"forward"`
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

	// Validate configuration
	if len(config.Listeners) == 0 {
		return nil, fmt.Errorf("no listeners configured")
	}

	for i, listener := range config.Listeners {
		if listener.Port <= 0 || listener.Port > 65535 {
			return nil, fmt.Errorf("listener %d: invalid port: %d (must be between 1 and 65535)", i+1, listener.Port)
		}

		if len(listener.Dispatch) == 0 {
			return nil, fmt.Errorf("listener %d: no dispatch rules configured", i+1)
		}

		for j, rule := range listener.Dispatch {
			if rule.Domain == "" {
				return nil, fmt.Errorf("listener %d, rule %d: domain cannot be empty", i+1, j+1)
			}
			if rule.ForwardTo == "" {
				return nil, fmt.Errorf("listener %d, rule %d: forward_to cannot be empty", i+1, j+1)
			}
			// Default to forward if type is not set or empty
			if rule.Type == "" {
				config.Listeners[i].Dispatch[j].Type = RuleTypeForward
			} else if rule.Type != RuleTypeForward && rule.Type != RuleTypeRedirect {
				return nil, fmt.Errorf("listener %d, rule %d: invalid type: %s (must be 'forward' or 'redirect')", i+1, j+1, rule.Type)
			}
		}
	}

	return &config, nil
}

// FindRule finds the dispatch rule for a given domain in a listener config
func (lc *ListenerConfig) FindRule(domain string) (DispatchRule, bool) {
	for _, rule := range lc.Dispatch {
		if rule.Domain == domain {
			return rule, true
		}
	}
	return DispatchRule{}, false
}

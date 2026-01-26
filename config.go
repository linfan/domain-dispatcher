package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// RuleType represents the type of dispatch rule
type RuleType string

const (
	RuleTypeForward  RuleType = "forward"
	RuleTypeRedirect RuleType = "redirect"
)

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

// Config represents the application configuration as a list of listeners
type Config []ListenerConfig

// LoadConfig loads configuration from a YAML file
func LoadConfig(filename string) (Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Validate configuration
	if len(config) == 0 {
		return nil, fmt.Errorf("no listeners configured")
	}

	for i, listener := range config {
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
			if rule.Type != RuleTypeForward && rule.Type != RuleTypeRedirect {
				// Default to forward if type is empty for backward compatibility if desired, 
				// but here we enforce it or default it. Let's default to forward if empty.
				if rule.Type == "" {
					listener.Dispatch[j].Type = RuleTypeForward
				} else {
					return nil, fmt.Errorf("listener %d, rule %d: invalid type: %s (must be 'forward' or 'redirect')", i+1, j+1, rule.Type)
				}
			}
		}
	}

	return config, nil
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
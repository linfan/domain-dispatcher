package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// DispatchRule represents a single domain forwarding rule
type DispatchRule struct {
	Domain    string `yaml:"domain"`
	ForwardTo string `yaml:"forward_to"`
}

// Config represents the application configuration
type Config struct {
	Port     int            `yaml:"port"`
	Dispatch []DispatchRule `yaml:"dispatch"`
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

	// Validate configuration
	if config.Port <= 0 || config.Port > 65535 {
		return nil, fmt.Errorf("invalid port: %d (must be between 1 and 65535)", config.Port)
	}

	if len(config.Dispatch) == 0 {
		return nil, fmt.Errorf("no dispatch rules configured")
	}

	for i, rule := range config.Dispatch {
		if rule.Domain == "" {
			return nil, fmt.Errorf("dispatch rule %d: domain cannot be empty", i+1)
		}
		if rule.ForwardTo == "" {
			return nil, fmt.Errorf("dispatch rule %d: forward_to cannot be empty", i+1)
		}
	}

	return &config, nil
}

// FindForwardTarget finds the forward target for a given domain
func (c *Config) FindForwardTarget(domain string) (string, bool) {
	for _, rule := range c.Dispatch {
		if rule.Domain == domain {
			return rule.ForwardTo, true
		}
	}
	return "", false
}
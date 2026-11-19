package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := LoadConfig("config.example.yaml")
	if err != nil {
		t.Fatalf("LoadConfig(config.example.yaml) error = %v", err)
	}
	if cfg.Transport.BufferSize != 32*1024 {
		t.Errorf("transport.buffer_size = %d, want %d", cfg.Transport.BufferSize, 32*1024)
	}
	if !cfg.AddProxyHeaders {
		t.Error("add_proxy_headers = false, want true")
	}
	if len(cfg.Listeners) == 0 {
		t.Fatal("no listeners loaded")
	}

	var mirrored *DispatchRule
	for i := range cfg.Listeners[0].Dispatch {
		if cfg.Listeners[0].Dispatch[i].Domain == "mirrored-api.com" {
			mirrored = &cfg.Listeners[0].Dispatch[i]
			break
		}
	}
	if mirrored == nil {
		t.Fatal("example config lost the mirrored-api.com rule")
	}
	if mirrored.Edit == nil || !mirrored.Edit.Enabled || mirrored.Edit.AddHeaders["X-Environment"] != "staging" {
		t.Fatalf("edit not parsed: %+v", mirrored.Edit)
	}
	if mirrored.Mirror == nil || !mirrored.Mirror.Enabled || mirrored.Mirror.Target == "" {
		t.Fatalf("mirror not parsed: %+v", mirrored.Mirror)
	}
	if mirrored.Mirror.Timeout != 5*time.Second || mirrored.Mirror.MaxBodyBytes != 1<<20 || mirrored.Mirror.QueueSize != 256 {
		t.Errorf("mirror defaults = %+v, want timeout 5s, max_body_bytes 1MiB, queue_size 256", mirrored.Mirror)
	}
}

func TestLoadConfigRejectsLegacyGlobalSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	legacy := `
global:
  debug: true
listeners:
  - port: 8080
    dispatch:
      - domain: api.example.com
        forward_to: http://127.0.0.1:9000
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), `the "global" section was removed`) {
		t.Fatalf("LoadConfig() error = %v, want legacy global section error", err)
	}
}

func TestLoadConfigRejectsLegacySourceIPBlacklist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	legacy := `
source_ip_blacklist:
  - 203.0.113.7
listeners:
  - port: 8080
    dispatch:
      - domain: api.example.com
        forward_to: http://127.0.0.1:9000
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), `"source_ip_blacklist" was moved to "access_control.source_ip.blacklist"`) {
		t.Fatalf("LoadConfig() error = %v, want legacy source_ip_blacklist error", err)
	}
}

func TestValidateSourceIPAccessControl(t *testing.T) {
	tests := []struct {
		name      string
		blacklist []string
		whitelist []string
		wantErr   string
	}{
		{
			name:      "invalid blacklist IP",
			blacklist: []string{"not-an-ip"},
			wantErr:   "access_control.source_ip.blacklist: invalid IP address",
		},
		{
			name:      "invalid blacklist CIDR",
			blacklist: []string{"10.0.0.0/33"},
			wantErr:   "access_control.source_ip.blacklist: invalid CIDR range",
		},
		{
			name:      "invalid whitelist IP",
			whitelist: []string{"nope"},
			wantErr:   "access_control.source_ip.whitelist: invalid IP address",
		},
		{
			name:      "valid entries",
			blacklist: []string{"203.0.113.7", "192.168.0.0/16"},
			whitelist: []string{"10.0.0.0/8", "::1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Listeners = []ListenerConfig{{
				Port: 8080,
				Type: ListenerTypeHTTP,
				Dispatch: []DispatchRule{{
					Domain:    "api.example.com",
					ForwardTo: "http://127.0.0.1:9000",
					Type:      RuleTypeForward,
				}},
			}}
			cfg.AccessControl.SourceIP = SourceIPAccessConfig{Blacklist: tt.blacklist, Whitelist: tt.whitelist}

			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSourceIPAccessControlDenied(t *testing.T) {
	tests := []struct {
		name      string
		blacklist []string
		whitelist []string
		ip        string
		want      bool
	}{
		{name: "empty lists allow", ip: "203.0.113.7", want: false},
		{name: "blacklist exact", blacklist: []string{"203.0.113.7"}, ip: "203.0.113.7", want: true},
		{name: "blacklist CIDR", blacklist: []string{"192.168.0.0/16"}, ip: "192.168.42.1", want: true},
		{name: "blacklist miss", blacklist: []string{"203.0.113.7"}, ip: "198.51.100.1", want: false},
		{name: "whitelist admits", whitelist: []string{"10.0.0.0/8"}, ip: "10.1.2.3", want: false},
		{name: "whitelist rejects others", whitelist: []string{"10.0.0.0/8"}, ip: "203.0.113.7", want: true},
		{name: "whitelist match overrides blacklist", blacklist: []string{"10.1.2.3"}, whitelist: []string{"10.0.0.0/8"}, ip: "10.1.2.3", want: false},
		{name: "non-whitelisted is denied even outside blacklist", blacklist: []string{"203.0.113.7"}, whitelist: []string{"10.0.0.0/8"}, ip: "203.0.113.7", want: true},
		{name: "unparsable IP is denied under whitelist", whitelist: []string{"10.0.0.0/8"}, ip: "not-an-ip", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := SourceIPAccessConfig{Blacklist: tt.blacklist, Whitelist: tt.whitelist}
			if got := ac.Denied(tt.ip); got != tt.want {
				t.Errorf("Denied(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestValidateEditAndMirror(t *testing.T) {
	tests := []struct {
		name    string
		rule    DispatchRule
		wantErr string
	}{
		{
			name: "edit on redirect rule",
			rule: DispatchRule{
				Domain:    "api.example.com",
				ForwardTo: "http://127.0.0.1:9000",
				Type:      RuleTypeRedirect,
				Edit:      &EditConfig{Enabled: true},
			},
			wantErr: "edit is only supported for forward rules",
		},
		{
			name: "mirror on mock rule",
			rule: DispatchRule{
				Domain: "api.example.com",
				Type:   RuleTypeMock,
				Mirror: &MirrorConfig{Enabled: true, Target: "http://127.0.0.1:9100"},
			},
			wantErr: "mirror is only supported for forward rules",
		},
		{
			name: "invalid edit header name",
			rule: DispatchRule{
				Domain:    "api.example.com",
				ForwardTo: "http://127.0.0.1:9000",
				Type:      RuleTypeForward,
				Edit:      &EditConfig{Enabled: true, AddHeaders: map[string]string{"Bad Header": "v"}},
			},
			wantErr: "invalid header name",
		},
		{
			name: "invalid edit header value",
			rule: DispatchRule{
				Domain:    "api.example.com",
				ForwardTo: "http://127.0.0.1:9000",
				Type:      RuleTypeForward,
				Edit:      &EditConfig{Enabled: true, AddHeaders: map[string]string{"X-Test": "a\rb"}},
			},
			wantErr: "must not contain CR",
		},
		{
			name: "mirror without target",
			rule: DispatchRule{
				Domain:    "api.example.com",
				ForwardTo: "http://127.0.0.1:9000",
				Type:      RuleTypeForward,
				Mirror:    &MirrorConfig{Enabled: true},
			},
			wantErr: "mirror.target is required",
		},
		{
			name: "mirror target scheme",
			rule: DispatchRule{
				Domain:    "api.example.com",
				ForwardTo: "http://127.0.0.1:9000",
				Type:      RuleTypeForward,
				Mirror:    &MirrorConfig{Enabled: true, Target: "ftp://mirror.example"},
			},
			wantErr: "must use http or https",
		},
		{
			name: "mirror target with query",
			rule: DispatchRule{
				Domain:    "api.example.com",
				ForwardTo: "http://127.0.0.1:9000",
				Type:      RuleTypeForward,
				Mirror:    &MirrorConfig{Enabled: true, Target: "http://127.0.0.1:9100/base?x=1"},
			},
			wantErr: "must not contain a query",
		},
		{
			name: "negative mirror queue",
			rule: DispatchRule{
				Domain:    "api.example.com",
				ForwardTo: "http://127.0.0.1:9000",
				Type:      RuleTypeForward,
				Mirror:    &MirrorConfig{Enabled: true, Target: "http://127.0.0.1:9100", QueueSize: -1},
			},
			wantErr: "mirror.queue_size must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Listeners = []ListenerConfig{{Port: 8080, Type: ListenerTypeHTTP, Dispatch: []DispatchRule{tt.rule}}}
			normalize(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNormalizeConfigDomains(t *testing.T) {
	cfg := defaultConfig()
	cfg.Listeners = []ListenerConfig{{
		Port: 8080,
		Dispatch: []DispatchRule{{
			Domain:    "API.Example.COM.",
			ForwardTo: "http://127.0.0.1:9000",
		}},
	}}

	normalize(cfg)

	if got := cfg.Listeners[0].Dispatch[0].Domain; got != "api.example.com" {
		t.Errorf("normalize() domain = %q, want %q", got, "api.example.com")
	}
}

func TestFindRuleNormalizesDomain(t *testing.T) {
	listener := ListenerConfig{Dispatch: []DispatchRule{
		{Domain: "API.Example.COM.", ForwardTo: "exact"},
		{Domain: "*", ForwardTo: "wildcard"},
	}}

	rule, found := listener.FindRule("api.example.com", "/")
	if !found || rule.ForwardTo != "exact" {
		t.Fatalf("FindRule() = %q, found=%v; want exact rule", rule.ForwardTo, found)
	}

	rule, found = listener.FindRule("EXAMPLE.com.", "/")
	if !found || rule.ForwardTo != "wildcard" {
		t.Fatalf("FindRule() = %q, found=%v; want wildcard rule", rule.ForwardTo, found)
	}
}

func TestCertificateForServerNameNormalizesSNI(t *testing.T) {
	exact := &tls.Certificate{}
	fallback := &tls.Certificate{}
	certs := map[string]*tls.Certificate{"api.example.com": exact}

	if got := certificateForServerName(certs, fallback, "API.Example.COM."); got != exact {
		t.Error("certificateForServerName() did not normalize the SNI name")
	}
	if got := certificateForServerName(certs, fallback, "unknown.example.com"); got != fallback {
		t.Error("certificateForServerName() did not fall back for an unknown SNI name")
	}
}

func TestFindRulePrefersLongestWildcardPrefix(t *testing.T) {
	tests := []struct {
		name  string
		rules []DispatchRule
		path  string
		want  string
	}{
		{
			name: "longer wildcard wins when listed last",
			rules: []DispatchRule{
				{Domain: "api.example.com", Path: "/api/*", ForwardTo: "short"},
				{Domain: "api.example.com", Path: "/api/v1/*", ForwardTo: "long"},
			},
			path: "/api/v1/users",
			want: "long",
		},
		{
			name: "longer wildcard wins when listed first",
			rules: []DispatchRule{
				{Domain: "api.example.com", Path: "/api/v1/*", ForwardTo: "long"},
				{Domain: "api.example.com", Path: "/api/*", ForwardTo: "short"},
			},
			path: "/api/v1/users",
			want: "long",
		},
		{
			name: "priority still overrides the longer wildcard",
			rules: []DispatchRule{
				{Domain: "api.example.com", Path: "/api/*", Priority: 1, ForwardTo: "short"},
				{Domain: "api.example.com", Path: "/api/v1/*", ForwardTo: "long"},
			},
			path: "/api/v1/users",
			want: "short",
		},
		{
			name: "exact path still beats the longer wildcard",
			rules: []DispatchRule{
				{Domain: "api.example.com", Path: "/api/v1/users", ForwardTo: "exact"},
				{Domain: "api.example.com", Path: "/api/v1/*", ForwardTo: "long"},
			},
			path: "/api/v1/users",
			want: "exact",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener := ListenerConfig{Dispatch: tt.rules}
			rule, found := listener.FindRule("api.example.com", tt.path)
			if !found {
				t.Fatal("FindRule() found no rule")
			}
			if rule.ForwardTo != tt.want {
				t.Errorf("FindRule() picked %q, want %q", rule.ForwardTo, tt.want)
			}
		})
	}
}

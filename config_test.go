package main

import (
	"crypto/tls"
	"testing"
)

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

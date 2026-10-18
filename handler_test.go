package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"domain-dispatcher/pkg/forwarder"
)

func TestLoadConfigRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	config := `
listeners:
  - port: 8080
    dispatch:
      - domain: api.example.com
        forward_to: http://127.0.0.1:9000
        rewrite:
          domain: backend.internal
          url:
            pattern: '^/api/v1/(.*)$'
            replacement: '/v2/$1'
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	rule := cfg.Listeners[0].Dispatch[0]
	if rule.Rewrite == nil || rule.Rewrite.Domain != "backend.internal" || rule.Rewrite.URL == nil {
		t.Fatalf("rewrite not parsed: %+v", rule.Rewrite)
	}
	if got := rule.Rewrite.URL.Apply("/api/v1/users/42"); got != "/v2/users/42" {
		t.Errorf("Apply() = %q, want %q", got, "/v2/users/42")
	}
}

func TestValidateRewrite(t *testing.T) {
	tests := []struct {
		name    string
		rule    DispatchRule
		wantErr string
	}{
		{
			name: "invalid regex",
			rule: DispatchRule{
				Domain:    "api.example.com",
				ForwardTo: "http://127.0.0.1:9000",
				Type:      RuleTypeForward,
				Rewrite:   &RewriteConfig{URL: &URLRewrite{Pattern: "(", Replacement: ""}},
			},
			wantErr: "invalid rewrite.url.pattern",
		},
		{
			name: "rewrite on redirect rule",
			rule: DispatchRule{
				Domain:    "api.example.com",
				ForwardTo: "http://127.0.0.1:9000",
				Type:      RuleTypeRedirect,
				Rewrite:   &RewriteConfig{Domain: "backend.internal"},
			},
			wantErr: "rewrite is only supported for forward rules",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Listeners = []ListenerConfig{{Port: 8080, Type: ListenerTypeHTTP, Dispatch: []DispatchRule{tt.rule}}}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestForwardRewrite(t *testing.T) {
	type captured struct {
		uri  string
		host string
	}
	got := make(chan captured, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- captured{uri: r.URL.RequestURI(), host: r.Host}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := defaultConfig()
	cfg.Listeners = []ListenerConfig{{
		Port: 8080,
		Type: ListenerTypeHTTP,
		Dispatch: []DispatchRule{{
			Domain:    "api.example.com",
			ForwardTo: upstream.URL,
			Type:      RuleTypeForward,
			Rewrite: &RewriteConfig{
				Domain: "backend.internal",
				URL: &URLRewrite{
					Pattern:     `^/api/v1/(.*)$`,
					Replacement: `/v2/$1`,
				},
			},
		}},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	engine := forwarder.New(cfg.Global.Forward.EngineConfig(cfg.Global.Forward.CompressionDisabled()))
	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/api/v1/users/42?verbose=1", nil)
	rec := httptest.NewRecorder()

	handleRequest(rec, req, cfg.Listeners[0], engine, &cfg.Global)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	select {
	case c := <-got:
		if c.uri != "/v2/users/42?verbose=1" {
			t.Errorf("upstream URI = %q, want %q", c.uri, "/v2/users/42?verbose=1")
		}
		if c.host != "backend.internal" {
			t.Errorf("upstream Host = %q, want %q", c.host, "backend.internal")
		}
	default:
		t.Fatal("upstream did not receive the request")
	}
}

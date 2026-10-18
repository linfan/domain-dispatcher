package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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

	serveHTTP(rec, req, cfg.Listeners[0], engine, &cfg.Global)

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

func TestBuildTargetURLJoinsBasePath(t *testing.T) {
	tests := []struct {
		name   string
		target string
		reqURL string
		want   string
	}{
		{
			name:   "base path and request path",
			target: "https://up.example/api",
			reqURL: "http://api.example.com/hello?verbose=1",
			want:   "https://up.example/api/hello?verbose=1",
		},
		{
			name:   "trailing slash in base path",
			target: "https://up.example/api/",
			reqURL: "http://api.example.com/hello",
			want:   "https://up.example/api/hello",
		},
		{
			name:   "no base path",
			target: "https://up.example",
			reqURL: "http://api.example.com/hello",
			want:   "https://up.example/hello",
		},
		{
			name:   "root base path",
			target: "https://up.example/",
			reqURL: "http://api.example.com/hello",
			want:   "https://up.example/hello",
		},
		{
			name:   "request root path",
			target: "https://up.example/api",
			reqURL: "http://api.example.com/",
			want:   "https://up.example/api/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.reqURL, nil)
			got, err := buildTargetURL(tt.target, r, nil)
			if err != nil {
				t.Fatalf("buildTargetURL() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("buildTargetURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildTargetURLPreservesEscapedPath(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://api.example.com/a%2Fb", nil)
	if r.URL.RawPath == "" {
		t.Fatal("test request lost its RawPath")
	}

	got, err := buildTargetURL("https://up.example/base", r, nil)
	if err != nil {
		t.Fatalf("buildTargetURL() error = %v", err)
	}
	if want := "https://up.example/base/a%2Fb"; got != want {
		t.Errorf("buildTargetURL() = %q, want %q", got, want)
	}
}

func TestBuildTargetURLRewriteJoinsBasePath(t *testing.T) {
	rw := &URLRewrite{Pattern: `^/api/v1/(.*)$`, Replacement: `/v2/$1`}
	rw.re = regexp.MustCompile(rw.Pattern)

	r := httptest.NewRequest(http.MethodGet, "http://api.example.com/api/v1/users?verbose=1", nil)
	got, err := buildTargetURL("https://up.example/base", r, &RewriteConfig{URL: rw})
	if err != nil {
		t.Fatalf("buildTargetURL() error = %v", err)
	}
	if want := "https://up.example/base/v2/users?verbose=1"; got != want {
		t.Errorf("buildTargetURL() = %q, want %q", got, want)
	}
}

func TestRequestHostNormalization(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"API.Example.COM:8080", "api.example.com"},
		{"example.com.", "example.com"},
		{"Example.COM.:443", "example.com"},
		{"[::1]:8080", "::1"},
		{"[::1]", "::1"},
	}

	for _, tt := range tests {
		r := &http.Request{Host: tt.host}
		if got := requestHost(r); got != tt.want {
			t.Errorf("requestHost(%q) = %q, want %q", tt.host, got, tt.want)
		}
	}
}

func TestRedactTarget(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://user:pass@up.example/api?token=secret#frag", "https://up.example/api"},
		{"mock", "mock"},
		{"-", "-"},
		{"", "-"},
	}

	for _, tt := range tests {
		if got := redactTarget(tt.in); got != tt.want {
			t.Errorf("redactTarget(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLoggingResponseWriterSupportsFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	lw := &loggingResponseWriter{ResponseWriter: rec}

	if _, err := lw.Write([]byte("event\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := http.NewResponseController(lw).Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if !rec.Flushed {
		t.Error("underlying writer was not flushed")
	}
	if lw.status != http.StatusOK || lw.bytes != int64(len("event\n")) {
		t.Errorf("recorded status=%d bytes=%d, want status=200 bytes=%d", lw.status, lw.bytes, len("event\n"))
	}
}

func TestServeHTTPLogsAccessAndPropagatesRequestID(t *testing.T) {
	upstreamID := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamID <- r.Header.Get("X-Request-ID")
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
		}},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	engine := forwarder.New(cfg.Global.Forward.EngineConfig(cfg.Global.Forward.CompressionDisabled()))

	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/hello?token=secret", nil)
	rec := httptest.NewRecorder()
	serveHTTP(rec, req, cfg.Listeners[0], engine, &cfg.Global)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	select {
	case id := <-upstreamID:
		if id == "" {
			t.Error("upstream did not receive X-Request-ID")
		}
	default:
		t.Fatal("upstream did not receive the request")
	}

	out := logs.String()
	if !strings.Contains(out, "resp_status=200") {
		t.Errorf("access log missing status: %s", out)
	}
	if strings.Contains(out, "token=secret") {
		t.Errorf("query string leaked into logs: %s", out)
	}
}

func TestServeHTTPLogs502OnForwardFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := upstream.URL
	upstream.Close()

	cfg := defaultConfig()
	cfg.Listeners = []ListenerConfig{{
		Port: 8080,
		Type: ListenerTypeHTTP,
		Dispatch: []DispatchRule{{
			Domain:    "api.example.com",
			ForwardTo: deadURL,
			Type:      RuleTypeForward,
		}},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	engine := forwarder.New(cfg.Global.Forward.EngineConfig(cfg.Global.Forward.CompressionDisabled()))

	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/fail?token=secret", nil)
	rec := httptest.NewRecorder()
	serveHTTP(rec, req, cfg.Listeners[0], engine, &cfg.Global)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	out := logs.String()
	if !strings.Contains(out, "resp_status=502") {
		t.Errorf("access log did not record the final 502: %s", out)
	}
	if strings.Contains(out, "token=secret") {
		t.Errorf("query string leaked into logs: %s", out)
	}
}

package main

import "testing"

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

package main

import (
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"domain-dispatcher/pkg/forwarder"
)

// handleRequest routes one incoming request through the listener's dispatch
// rules and logs a per-request summary line.
func handleRequest(w http.ResponseWriter, r *http.Request, listener ListenerConfig, fwd *forwarder.Forwarder, global *GlobalConfig) {
	start := time.Now()
	host := requestHost(r)

	// Check source IP blacklist
	if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && forwarder.IPBlacklisted(global.SourceIPBlacklist, clientIP) {
		if global.Debug {
			log.Printf("[BLOCKED] source=%s - IP is blacklisted", r.RemoteAddr)
		}
		http.Error(w, "Forbidden: request is not allowed", http.StatusForbidden)
		return
	}

	// Find dispatch rule
	rule, found := listener.FindRule(host, r.URL.Path)
	if !found {
		log.Printf("[Port %d] No mapping found for domain: %s", listener.Port, host)
		http.NotFound(w, r)
		logSummary(r, host, http.StatusNotFound, 0, time.Since(start))
		return
	}

	switch rule.Type {
	case RuleTypeMock:
		if global.Debug {
			log.Printf("[Port %d] Mock response for %s (status=%d)", listener.Port, host, rule.MockStatus)
		}
		for k, v := range rule.MockHeaders {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rule.MockStatus)
		var written int64
		if rule.MockBody != "" {
			n, err := w.Write([]byte(rule.MockBody))
			written = int64(n)
			if err != nil {
				log.Printf("[Port %d] Failed to write mock body: %v", listener.Port, err)
			}
		}
		logSummary(r, host, rule.MockStatus, written, time.Since(start))
	case RuleTypeRedirect:
		targetURL, err := buildTargetURL(rule.ForwardTo, r)
		if err != nil {
			log.Printf("[Port %d] Invalid target URL %s: %v", listener.Port, rule.ForwardTo, err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if global.Debug {
			log.Printf("[Port %d] Redirecting %s to %s", listener.Port, host, targetURL)
		}
		http.Redirect(w, r, targetURL, http.StatusFound) // 302 redirect
		logSummary(r, targetURL, http.StatusFound, 0, time.Since(start))
	case RuleTypeForward:
		targetURL, err := buildTargetURL(rule.ForwardTo, r)
		if err != nil {
			log.Printf("[Port %d] Invalid target URL %s: %v", listener.Port, rule.ForwardTo, err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if global.AddProxyHeaders {
			scheme := "http"
			if listener.Type == ListenerTypeHTTPS {
				scheme = "https"
			}
			forwarder.AddForwardedHeaders(r, scheme)
		}
		if global.Debug {
			log.Printf("[Port %d] Forwarding %s to %s", listener.Port, host, targetURL)
		}
		result, fwdErr := fwd.ForwardRequest(w, r, targetURL, rule.RemoveHeaders)
		logSummary(r, targetURL, result.StatusCode, result.RespSize, result.Duration)
		if fwdErr != nil {
			log.Printf("[Port %d] Failed to forward request to %s: %v", listener.Port, targetURL, fwdErr)
			if !errors.Is(fwdErr, forwarder.ErrResponseStarted) {
				http.Error(w, "Bad Gateway: upstream request failed", http.StatusBadGateway)
			}
		}
	default:
		log.Printf("[Port %d] Unknown rule type for %s: %s", listener.Port, host, rule.Type)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// logSummary prints the unified per-request summary line.
func logSummary(r *http.Request, target string, status int, respSize int64, duration time.Duration) {
	log.Printf("[%s] source=%s target=%s req_size=%d resp_status=%d resp_size=%d duration=%v",
		r.Method, r.RemoteAddr, target, r.ContentLength, status, respSize, duration)
}

// requestHost extracts the hostname from the Host header, dropping any port
// ("example.com:8080", "[::1]:8080").
func requestHost(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return strings.Trim(r.Host, "[]")
}

// buildTargetURL combines the rule's target base URL with the original request
// path and query.
func buildTargetURL(target string, r *http.Request) (string, error) {
	targetURL, err := url.Parse(target)
	if err != nil {
		return "", err
	}

	// Build the target URL with original path and query
	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery
	return targetURL.String(), nil
}

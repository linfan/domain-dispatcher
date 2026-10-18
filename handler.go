package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"domain-dispatcher/pkg/forwarder"
)

// serveHTTP handles one request and prints the unified access log line once the
// response has been produced, so every branch is covered exactly once.
func serveHTTP(w http.ResponseWriter, r *http.Request, listener ListenerConfig, fwd *forwarder.Forwarder, global *GlobalConfig) {
	start := time.Now()
	reqID := ensureRequestID(r)
	lw := &loggingResponseWriter{ResponseWriter: w}

	target := handleRequest(lw, r, reqID, listener, fwd, global)

	status := lw.status
	if status == 0 {
		status = http.StatusOK
	}
	logSummary(r, reqID, target, status, lw.bytes, time.Since(start))
}

// handleRequest routes one request through the listener's dispatch rules and
// returns the upstream target for the access log ("-" when none was selected).
func handleRequest(w http.ResponseWriter, r *http.Request, reqID string, listener ListenerConfig, fwd *forwarder.Forwarder, global *GlobalConfig) string {
	host := requestHost(r)

	// Check source IP blacklist
	if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && forwarder.IPBlacklisted(global.SourceIPBlacklist, clientIP) {
		if global.Debug {
			log.Printf("[Port %d] id=%s [BLOCKED] source=%s - IP is blacklisted", listener.Port, reqID, r.RemoteAddr)
		}
		http.Error(w, "Forbidden: request is not allowed", http.StatusForbidden)
		return "-"
	}

	// Find dispatch rule
	rule, found := listener.FindRule(host, r.URL.Path)
	if !found {
		log.Printf("[Port %d] id=%s No mapping found for domain: %s", listener.Port, reqID, host)
		http.NotFound(w, r)
		return "-"
	}

	switch rule.Type {
	case RuleTypeMock:
		if global.Debug {
			log.Printf("[Port %d] id=%s Mock response for %s (status=%d)", listener.Port, reqID, host, rule.MockStatus)
		}
		for k, v := range rule.MockHeaders {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rule.MockStatus)
		if rule.MockBody != "" {
			if _, err := w.Write([]byte(rule.MockBody)); err != nil {
				log.Printf("[Port %d] id=%s Failed to write mock body: %v", listener.Port, reqID, err)
			}
		}
		return "mock"
	case RuleTypeRedirect:
		targetURL, err := buildTargetURL(rule.ForwardTo, r, nil)
		if err != nil {
			log.Printf("[Port %d] id=%s Invalid target URL %s: %v", listener.Port, reqID, redactTarget(rule.ForwardTo), err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return "-"
		}
		if global.Debug {
			log.Printf("[Port %d] id=%s Redirecting %s to %s", listener.Port, reqID, host, redactTarget(targetURL))
		}
		http.Redirect(w, r, targetURL, http.StatusFound) // 302 redirect
		return targetURL
	case RuleTypeForward:
		targetURL, err := buildTargetURL(rule.ForwardTo, r, rule.Rewrite)
		if err != nil {
			log.Printf("[Port %d] id=%s Invalid target URL %s: %v", listener.Port, reqID, redactTarget(rule.ForwardTo), err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return "-"
		}
		if global.AddProxyHeaders {
			scheme := "http"
			if listener.Type == ListenerTypeHTTPS {
				scheme = "https"
			}
			forwarder.AddForwardedHeaders(r, scheme)
		}
		if global.Debug {
			log.Printf("[Port %d] id=%s Forwarding %s to %s", listener.Port, reqID, host, redactTarget(targetURL))
		}
		hostOverride := ""
		if rule.Rewrite != nil {
			hostOverride = rule.Rewrite.Domain
		}
		if _, fwdErr := fwd.ForwardRequestWithHost(w, r, targetURL, hostOverride, rule.RemoveHeaders); fwdErr != nil {
			log.Printf("[Port %d] id=%s Failed to forward request to %s: %v", listener.Port, reqID, redactTarget(targetURL), fwdErr)
			if !errors.Is(fwdErr, forwarder.ErrResponseStarted) {
				http.Error(w, "Bad Gateway: upstream request failed", http.StatusBadGateway)
			}
		}
		return targetURL
	default:
		log.Printf("[Port %d] id=%s Unknown rule type for %s: %s", listener.Port, reqID, host, rule.Type)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return "-"
	}
}

// loggingResponseWriter records the final status code and the number of bytes
// written so the access log covers mock, redirect, forward, 403, 404 and 500
// responses uniformly.
type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *loggingResponseWriter) WriteHeader(code int) {
	// Informational responses other than 101 do not finalize the status.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// ReadFrom preserves the underlying writer's ReaderFrom fast path (sendfile).
func (w *loggingResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(src)
		w.bytes += n
		return n, err
	}
	n, err := io.Copy(w.ResponseWriter, src)
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the wrapped writer's Flusher and
// Hijacker, which the streaming response path relies on.
func (w *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// logSummary prints the unified per-request access log line. The target is
// stripped of userinfo and query so credentials never reach the log.
func logSummary(r *http.Request, reqID, target string, status int, respSize int64, duration time.Duration) {
	log.Printf("[%s] id=%s source=%s host=%s path=%s target=%s req_size=%d resp_status=%d resp_size=%d duration=%v",
		r.Method, reqID, r.RemoteAddr, requestHost(r), r.URL.EscapedPath(), redactTarget(target), r.ContentLength, status, respSize, duration)
}

// redactTarget strips the query string, fragment and userinfo from a URL before
// it is written to the log.
func redactTarget(target string) string {
	if target == "" {
		return "-"
	}
	u, err := url.Parse(target)
	if err != nil {
		if i := strings.IndexAny(target, "?#"); i >= 0 {
			return target[:i]
		}
		return target
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// ensureRequestID reuses a safe client-supplied X-Request-ID, generates a new
// one otherwise, and forwards it upstream for log correlation.
func ensureRequestID(r *http.Request) string {
	id := r.Header.Get("X-Request-ID")
	if !validRequestID(id) {
		id = newRequestID()
	}
	r.Header.Set("X-Request-ID", id)
	return id
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// requestHost extracts the hostname from the Host header, dropping any port
// ("example.com:8080", "[::1]:8080") and normalizing DNS case and a trailing dot.
func requestHost(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return normalizeHostname(h)
	}
	return normalizeHostname(strings.Trim(r.Host, "[]"))
}

// buildTargetURL joins the rule's target base URL with the original request
// path and query. When rewrite is set, its URL substitution is applied to the
// decoded request path first. Without a rewrite, the incoming path's raw
// encoding is preserved.
func buildTargetURL(target string, r *http.Request, rewrite *RewriteConfig) (string, error) {
	targetURL, err := url.Parse(target)
	if err != nil {
		return "", err
	}

	reqPath, reqRawPath := r.URL.Path, r.URL.RawPath
	if rewrite != nil && rewrite.URL != nil {
		reqPath = rewrite.URL.Apply(reqPath)
		reqRawPath = ""
	}

	targetURL.Path, targetURL.RawPath = joinURLPath(targetURL.Path, targetURL.EscapedPath(), reqPath, reqRawPath)
	targetURL.RawQuery = r.URL.RawQuery
	return targetURL.String(), nil
}

// joinURLPath joins a base path and a request path, keeping the raw (escaped)
// form when it differs from the default escaping of the joined path.
func joinURLPath(basePath, baseEscaped, reqPath, reqRawPath string) (path, rawPath string) {
	reqEscaped := reqRawPath
	if reqEscaped == "" {
		reqEscaped = (&url.URL{Path: reqPath}).EscapedPath()
	}
	path = joinPathElements(basePath, reqPath)
	rawPath = joinPathElements(baseEscaped, reqEscaped)
	if rawPath == "" || (&url.URL{Path: path}).EscapedPath() == rawPath {
		return path, ""
	}
	return path, rawPath
}

func joinPathElements(base, elem string) string {
	if elem == "" {
		return base
	}
	if base == "" {
		if strings.HasPrefix(elem, "/") {
			return elem
		}
		return "/" + elem
	}
	baseSlash := strings.HasSuffix(base, "/")
	elemSlash := strings.HasPrefix(elem, "/")
	switch {
	case baseSlash && elemSlash:
		return base + elem[1:]
	case !baseSlash && !elemSlash:
		return base + "/" + elem
	default:
		return base + elem
	}
}

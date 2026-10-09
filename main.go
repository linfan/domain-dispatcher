package main

import (
	"domain-dispatcher/pkg/forwarder"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Printf("Usage: %s <config.yaml>\n", os.Args[0])
		os.Exit(1)
	}

	configFile := os.Args[1]

	// Load configuration
	config, err := LoadConfig(configFile)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Loaded %d listeners", len(config.Listeners))

	// Create forwarder with configuration
	fwdConfig := forwarder.Config{
		MaxIdleConns:          config.Global.Forward.MaxIdleConns,
		MaxIdleConnsPerHost:   config.Global.Forward.MaxIdleConnsPerHost,
		MaxConnsPerHost:       config.Global.Forward.MaxConnsPerHost,
		IdleConnTimeout:       config.Global.Forward.IdleConnTimeout,
		TLSHandshakeTimeout:   config.Global.Forward.TLSHandshakeTimeout,
		ExpectContinueTimeout: config.Global.Forward.ExpectContinueTimeout,
		ResponseHeaderTimeout: config.Global.Forward.ResponseHeaderTimeout,
		DialTimeout:           config.Global.Forward.DialTimeout,
		DialKeepAlive:         config.Global.Forward.DialKeepAlive,
		RequestTimeout:        config.Global.Forward.RequestTimeout,
		ForceAttemptHTTP2:     config.Global.Forward.ForceAttemptHTTP2,
	}
	fwd := forwarder.New(fwdConfig)

	// Channel to capture errors from goroutines
	errChan := make(chan error, len(config.Listeners))

	for _, listener := range config.Listeners {
		go func(l ListenerConfig, sslCert, sslKey string) {
			protoType := "HTTP"
			if l.Type == ListenerTypeHTTPS {
				protoType = "HTTPS"
			}
			log.Printf("Starting %s domain dispatcher on port %d with %d rules", protoType, l.Port, len(l.Dispatch))

			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				handleRequest(w, r, l, fwd)
			})

			addr := fmt.Sprintf(":%d", l.Port)
			log.Printf("Server listening on %s (%s)", addr, protoType)

			var err error
			if l.Type == ListenerTypeHTTPS {
				err = http.ListenAndServeTLS(addr, sslCert, sslKey, mux)
			} else {
				err = http.ListenAndServe(addr, mux)
			}
			if err != nil {
				errChan <- fmt.Errorf("server on port %d failed: %w", l.Port, err)
			}
		}(listener, config.Global.SSL.CertFile, config.Global.SSL.KeyFile)
	}

	// Wait for any server to fail
	if err := <-errChan; err != nil {
		log.Fatalf("Fatal error: %v", err)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request, listener ListenerConfig, fwd *forwarder.Forwarder) {
	// Extract domain from Host header
	host := r.Host
	// Remove port if present
	if colonIndex := strings.Index(host, ":"); colonIndex != -1 {
		host = host[:colonIndex]
	}

	// Find dispatch rule
	rule, found := listener.FindRule(host)
	if !found {
		log.Printf("[Port %d] No mapping found for domain: %s", listener.Port, host)
		http.NotFound(w, r)
		return
	}

	switch rule.Type {
	case RuleTypeMock:
		log.Printf("[Port %d] Mock response for %s (status=%d)", listener.Port, host, rule.MockStatus)
		for k, v := range rule.MockHeaders {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rule.MockStatus)
		if rule.MockBody != "" {
			w.Write([]byte(rule.MockBody))
		}
	case RuleTypeRedirect:
		targetURL, err := buildTargetURL(rule.ForwardTo, r)
		if err != nil {
			log.Printf("[Port %d] Invalid target URL %s: %v", listener.Port, rule.ForwardTo, err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		log.Printf("[Port %d] Redirecting %s to %s", listener.Port, host, targetURL)
		http.Redirect(w, r, targetURL, http.StatusFound) // 302 redirect
	case RuleTypeForward:
		targetURL, err := buildTargetURL(rule.ForwardTo, r)
		if err != nil {
			log.Printf("[Port %d] Invalid target URL %s: %v", listener.Port, rule.ForwardTo, err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		log.Printf("[Port %d] Forwarding %s to %s", listener.Port, host, targetURL)
		if err := fwd.ForwardRequest(w, r, targetURL, rule.RemoveHeaders); err != nil {
			log.Printf("[Port %d] Failed to forward request to %s: %v", listener.Port, targetURL, err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
	default:
		log.Printf("[Port %d] Unknown rule type for %s: %s", listener.Port, host, rule.Type)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

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

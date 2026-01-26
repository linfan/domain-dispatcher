package main

import (
	"bytes"
	"fmt"
	"io"
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

	log.Printf("Loaded %d listeners", len(config))

	// Channel to capture errors from goroutines
	errChan := make(chan error, len(config))

	for _, listener := range config {
		go func(l ListenerConfig) {
			log.Printf("Starting domain dispatcher on port %d with %d rules", l.Port, len(l.Dispatch))
			
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				handleRequest(w, r, l)
			})

			addr := fmt.Sprintf(":%d", l.Port)
			log.Printf("Server listening on %s", addr)
			if err := http.ListenAndServe(addr, mux); err != nil {
				errChan <- fmt.Errorf("server on port %d failed: %w", l.Port, err)
			}
		}(listener)
	}

	// Wait for any server to fail
	if err := <-errChan; err != nil {
		log.Fatalf("Fatal error: %v", err)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request, listener ListenerConfig) {
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

	targetURL, err := buildTargetURL(rule.ForwardTo, r)
	if err != nil {
		log.Printf("[Port %d] Invalid target URL %s: %v", listener.Port, rule.ForwardTo, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	switch rule.Type {
	case RuleTypeRedirect:
		log.Printf("[Port %d] Redirecting %s to %s", listener.Port, host, targetURL)
		http.Redirect(w, r, targetURL, http.StatusFound) // 302 redirect
	case RuleTypeForward:
		log.Printf("[Port %d] Forwarding %s to %s", listener.Port, host, targetURL)
		if err := forwardRequest(w, r, targetURL); err != nil {
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
	// Note: This preserves the original behavior of overwriting target path with request path
	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery
	return targetURL.String(), nil
}

func forwardRequest(w http.ResponseWriter, r *http.Request, targetURLStr string) error {
	// Read request body
	var body bytes.Buffer
	if r.Body != nil {
		if _, err := io.Copy(&body, r.Body); err != nil {
			return fmt.Errorf("failed to read request body: %w", err)
		}
	}

	// Create new request
	req, err := http.NewRequest(r.Method, targetURLStr, &body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Copy headers (except Host which should be set to target host)
	for key, values := range r.Header {
		if key == "Host" {
			continue // Don't copy Host header
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Parse target URL to get host
	u, _ := url.Parse(targetURLStr)
	req.Host = u.Host

	// Send request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Set status code
	w.WriteHeader(resp.StatusCode)

	// Copy response body
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("failed to copy response body: %w", err)
	}

	return nil
}
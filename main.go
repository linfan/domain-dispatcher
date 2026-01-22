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

	log.Printf("Starting domain dispatcher on port %d", config.Port)
	log.Printf("Loaded %d dispatch rules", len(config.Dispatch))

	// Create HTTP handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleRequest(w, r, config)
	})

	// Start server
	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("Server listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request, config *Config) {
	// Extract domain from Host header
	host := r.Host
	// Remove port if present
	if colonIndex := strings.Index(host, ":"); colonIndex != -1 {
		host = host[:colonIndex]
	}

	// Find forwarding target
	forwardTo, found := config.FindForwardTarget(host)
	if !found {
		log.Printf("No mapping found for domain: %s", host)
		http.NotFound(w, r)
		return
	}

	// Forward the request
	if err := forwardRequest(w, r, forwardTo); err != nil {
		log.Printf("Failed to forward request to %s: %v", forwardTo, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

func forwardRequest(w http.ResponseWriter, r *http.Request, forwardTo string) error {
	// Parse the forward URL
	targetURL, err := url.Parse(forwardTo)
	if err != nil {
		return fmt.Errorf("invalid forward URL: %w", err)
	}

	// Build the target URL with original path and query
	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery

	// Read request body
	var body bytes.Buffer
	if r.Body != nil {
		if _, err := io.Copy(&body, r.Body); err != nil {
			return fmt.Errorf("failed to read request body: %w", err)
		}
	}

	// Create new request
	req, err := http.NewRequest(r.Method, targetURL.String(), &body)
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

	// Set Host header to target host
	req.Host = targetURL.Host

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
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"domain-dispatcher/pkg/forwarder"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Version and BuildTime are injected at build time via -ldflags.
var (
	Version   = "dev"
	BuildTime = "unknown"
)

var (
	configFile  = flag.String("config", "", "Path to config file (YAML), default config.yaml")
	versionFlag = flag.Bool("version", false, "Print version and exit")
)

func main() {
	flag.StringVar(configFile, "c", "", "Path to config file (YAML) (shorthand)")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("domain-dispatcher %s (built %s)\n", Version, BuildTime)
		return
	}

	configPath := *configFile
	if configPath == "" {
		configPath = "config.yaml"
	}

	// Load configuration
	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Redirect logs to file if configured
	if cfg.Global.Log.Enabled() {
		logFile := filepath.Join(cfg.Global.Log.Dir, cfg.Global.Log.Filename)
		logger := &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    cfg.Global.Log.MaxSize,
			MaxBackups: cfg.Global.Log.MaxBackups,
		}
		// lumberjack reports open failures through Write and log.Logger discards
		// write errors silently, so probe the target before switching output.
		if _, err := logger.Write(nil); err != nil {
			log.Printf("WARNING: cannot open log file %s: %v; logging to stderr", logFile, err)
		} else {
			log.SetOutput(logger)
			log.Printf("Logging to file: %s (max_size=%dMB, max_backups=%d)",
				logFile, cfg.Global.Log.MaxSize, cfg.Global.Log.MaxBackups)
		}
	}

	log.Printf("Domain Dispatcher %s (built %s)", Version, BuildTime)
	log.Printf("Loaded configuration from %s", configPath)
	log.Printf("Loaded %d listeners", len(cfg.Listeners))

	// Create the forwarding engine with the shared configuration. The engine
	// strips the client Accept-Encoding so that a compressing upstream cannot
	// buffer for example SSE streams.
	engine := forwarder.New(cfg.Global.Forward.EngineConfig(cfg.Global.Forward.CompressionDisabled()))

	// Build one HTTP server per listener
	servers := make([]*http.Server, 0, len(cfg.Listeners))
	for _, listener := range cfg.Listeners {
		protoType := "HTTP"
		if listener.Type == ListenerTypeHTTPS {
			protoType = "HTTPS"
		}
		log.Printf("Starting %s domain dispatcher on port %d with %d rules", protoType, listener.Port, len(listener.Dispatch))

		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			serveHTTP(w, r, listener, engine, &cfg.Global)
		})

		srv := &http.Server{
			Addr:    fmt.Sprintf(":%d", listener.Port),
			Handler: mux,
			// No Read/Write timeout: request and response bodies may stream
			// for as long as the upstream and the watchdogs allow.
			ReadTimeout:       0,
			WriteTimeout:      0,
			IdleTimeout:       cfg.Global.Server.IdleTimeout,
			ReadHeaderTimeout: cfg.Global.Server.ReadHeaderTimeout,
			MaxHeaderBytes:    cfg.Global.Server.MaxHeaderBytes,
		}
		if listener.Type == ListenerTypeHTTPS {
			tlsConfig, err := buildTLSConfig(listener)
			if err != nil {
				log.Fatalf("Failed to build TLS configuration for port %d: %v", listener.Port, err)
			}
			srv.TLSConfig = tlsConfig
		}
		servers = append(servers, srv)
	}

	// Register the signal handler before starting the listeners so a signal
	// arriving during startup still triggers a graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start all listeners; a serve error is reported exactly once.
	errCh := make(chan error, len(servers))
	for i, listener := range cfg.Listeners {
		go func(srv *http.Server, l ListenerConfig) {
			var err error
			if l.Type == ListenerTypeHTTPS {
				// Certificates are selected per domain by srv.TLSConfig.GetCertificate.
				err = srv.ListenAndServeTLS("", "")
			} else {
				err = srv.ListenAndServe()
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("server on port %d failed: %w", l.Port, err)
			}
		}(servers[i], listener)
	}

	exitCode := 0
	select {
	case <-sigCh:
		log.Println("Shutting down domain dispatcher...")
	case serveErr := <-errCh:
		log.Printf("Fatal error: %v", serveErr)
		exitCode = 1
	}

	// Graceful shutdown: all listeners share a single timeout budget.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Global.Server.ShutdownTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Add(1)
		go func(srv *http.Server) {
			defer wg.Done()
			if err := srv.Shutdown(ctx); err != nil {
				log.Printf("Error shutting down server on %s: %v", srv.Addr, err)
			}
		}(srv)
	}
	wg.Wait()

	if exitCode != 0 {
		os.Exit(exitCode)
	}
	log.Println("Server stopped")
}

// buildTLSConfig loads the per-domain certificates from the listener's dispatch
// rules and returns a tls.Config that selects a certificate by the client's SNI
// server name. The certificate of a "*" wildcard-domain rule, if any, is the
// fallback for unknown server names and clients that send no SNI; without one,
// such connections fail the TLS handshake.
func buildTLSConfig(listener ListenerConfig) (*tls.Config, error) {
	certs := make(map[string]*tls.Certificate)
	var fallback *tls.Certificate
	for _, rule := range listener.Dispatch {
		if rule.TLS == nil {
			continue
		}
		cert, err := tls.LoadX509KeyPair(rule.TLS.CertFile, rule.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load tls certificate for domain %q: %w", rule.Domain, err)
		}
		if normalizeHostname(rule.Domain) == "*" {
			fallback = &cert
			continue
		}
		domain := normalizeHostname(rule.Domain)
		if _, ok := certs[domain]; !ok {
			certs[domain] = &cert
		}
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return certificateForServerName(certs, fallback, hello.ServerName), nil
		},
	}, nil
}

// certificateForServerName selects the certificate for a normalized SNI name,
// falling back to the wildcard rule's certificate when none matches.
func certificateForServerName(certs map[string]*tls.Certificate, fallback *tls.Certificate, serverName string) *tls.Certificate {
	if cert, ok := certs[normalizeHostname(serverName)]; ok {
		return cert
	}
	return fallback
}

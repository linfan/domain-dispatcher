# Makefile for Domain Dispatcher

.PHONY: build run clean test fmt vet deps help

# Binary name and paths
BINARY_NAME=domain-dispatcher
DIST_DIR=dist
BINARY_PATH=$(DIST_DIR)/$(BINARY_NAME)
CONFIG_FILE=config.yaml

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOFMT=$(GOCMD) fmt
GOVET=$(GOCMD) vet
GOMOD=$(GOCMD) mod

# Build the project
build:
	mkdir -p $(DIST_DIR)
	$(GOBUILD) -o $(BINARY_PATH) -v

# Run the application
run: build
	./$(BINARY_PATH) $(CONFIG_FILE)

# Clean build artifacts
clean:
	$(GOCLEAN)
	rm -rf $(DIST_DIR)

# Run tests
test:
	$(GOTEST) -v ./...

# Format code
fmt:
	$(GOFMT) ./...

# Run go vet
vet:
	$(GOVET) ./...

# Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Install dependencies
install-deps: deps

# Run all checks (format, vet, test)
check: fmt vet test

# Build for multiple platforms (optional)
build-linux:
	mkdir -p $(DIST_DIR)
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(DIST_DIR)/$(BINARY_NAME)-linux-amd64 -v

build-windows:
	mkdir -p $(DIST_DIR)
	GOOS=windows GOARCH=amd64 $(GOBUILD) -o $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe -v

build-mac:
	mkdir -p $(DIST_DIR)
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 -v

# Cross-platform build
build-all: build-linux build-windows build-mac

# Development setup
dev-setup: deps fmt vet

# Help target
help:
	@echo "Available targets:"
	@echo "  build         - Build the application"
	@echo "  run           - Build and run the application"
	@echo "  clean         - Remove build artifacts"
	@echo "  test          - Run tests"
	@echo "  fmt           - Format Go code"
	@echo "  vet           - Run go vet"
	@echo "  deps          - Download and tidy dependencies"
	@echo "  check         - Run fmt, vet, and test"
	@echo "  build-linux   - Build for Linux"
	@echo "  build-windows - Build for Windows"
	@echo "  build-mac     - Build for macOS"
	@echo "  build-all     - Build for all platforms"
	@echo "  dev-setup     - Setup development environment"
	@echo "  help          - Show this help message"
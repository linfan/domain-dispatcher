.PHONY: all build clean test run install help
.PHONY: build-linux build-linux-arm64 build-macos build-macos-arm64 build-windows build-all release

# Build output directory
BUILD_DIR := build
DIST_DIR := dist

# Binary name
BINARY_NAME := domain-dispatcher

# Local runtime configuration used by `make run` (gitignored; copy from the template)
CONFIG_FILE := config.yaml

# Configuration template bundled with cross-platform builds
CONFIG_TEMPLATE := config.example.yaml

# Version info
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')

# Go build flags for optimization
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)
GCFLAGS := -B

all: build

# Help target
help:
	@echo "Domain Dispatcher - Makefile Commands"
	@echo ""
	@echo "Development:"
	@echo "  make build              - Build for current platform"
	@echo "  make clean              - Clean build artifacts"
	@echo "  make test               - Run tests"
	@echo "  make run                - Run dispatcher with config.yaml"
	@echo ""
	@echo "Cross-platform Builds:"
	@echo "  make build-linux        - Build for Linux (amd64)"
	@echo "  make build-linux-arm64  - Build for Linux (arm64)"
	@echo "  make build-macos        - Build for macOS (Intel)"
	@echo "  make build-macos-arm64  - Build for macOS (Apple Silicon)"
	@echo "  make build-windows      - Build for Windows (amd64)"
	@echo "  make build-all          - Build for all platforms"
	@echo "  make release            - Build and create release archives"
	@echo ""
	@echo "Installation:"
	@echo "  make install            - Install to GOPATH/bin"
	@echo ""
	@echo "Version: $(VERSION)"
	@echo "Build Time: $(BUILD_TIME)"

build:
	@mkdir -p $(BUILD_DIR)
	go build -ldflags="$(LDFLAGS)" -gcflags="$(GCFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) .

clean:
	rm -rf $(BUILD_DIR)
	rm -rf $(DIST_DIR)
	rm -f $(BINARY_NAME)

# Run targets
run: build
	$(BUILD_DIR)/$(BINARY_NAME) -c $(CONFIG_FILE)

test:
	go test ./...

# Install to GOPATH/bin
install: build
	cp $(BUILD_DIR)/$(BINARY_NAME) $(GOPATH)/bin/$(BINARY_NAME)

# Cross-platform builds
build-linux:
	@echo "Building for Linux (amd64)..."
	@mkdir -p $(DIST_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -gcflags="$(GCFLAGS)" -o $(DIST_DIR)/linux-amd64/$(BINARY_NAME) .
	@cp $(CONFIG_TEMPLATE) $(DIST_DIR)/linux-amd64/
	@echo "Linux build complete: $(DIST_DIR)/linux-amd64/"

build-linux-arm64:
	@echo "Building for Linux (arm64)..."
	@mkdir -p $(DIST_DIR)/linux-arm64
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -gcflags="$(GCFLAGS)" -o $(DIST_DIR)/linux-arm64/$(BINARY_NAME) .
	@cp $(CONFIG_TEMPLATE) $(DIST_DIR)/linux-arm64/
	@echo "Linux ARM64 build complete: $(DIST_DIR)/linux-arm64/"

build-macos:
	@echo "Building for macOS (amd64)..."
	@mkdir -p $(DIST_DIR)/darwin-amd64
	GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -gcflags="$(GCFLAGS)" -o $(DIST_DIR)/darwin-amd64/$(BINARY_NAME) .
	@cp $(CONFIG_TEMPLATE) $(DIST_DIR)/darwin-amd64/
	@echo "macOS (Intel) build complete: $(DIST_DIR)/darwin-amd64/"

build-macos-arm64:
	@echo "Building for macOS (Apple Silicon)..."
	@mkdir -p $(DIST_DIR)/darwin-arm64
	GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -gcflags="$(GCFLAGS)" -o $(DIST_DIR)/darwin-arm64/$(BINARY_NAME) .
	@cp $(CONFIG_TEMPLATE) $(DIST_DIR)/darwin-arm64/
	@echo "macOS (Apple Silicon) build complete: $(DIST_DIR)/darwin-arm64/"

build-windows:
	@echo "Building for Windows (amd64)..."
	@mkdir -p $(DIST_DIR)/windows-amd64
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -gcflags="$(GCFLAGS)" -o $(DIST_DIR)/windows-amd64/$(BINARY_NAME).exe .
	@cp $(CONFIG_TEMPLATE) $(DIST_DIR)/windows-amd64/
	@echo "Windows build complete: $(DIST_DIR)/windows-amd64/"

# Build for all platforms
build-all: build-linux build-linux-arm64 build-macos build-macos-arm64 build-windows
	@echo "All platform builds complete!"
	@echo "Output directory: $(DIST_DIR)/"

# Create release archives
release: build-all
	@echo "Creating release archives..."
	@cd $(DIST_DIR)/linux-amd64 && tar -czf ../$(BINARY_NAME)-$(VERSION)-linux-amd64.tar.gz *
	@cd $(DIST_DIR)/linux-arm64 && tar -czf ../$(BINARY_NAME)-$(VERSION)-linux-arm64.tar.gz *
	@cd $(DIST_DIR)/darwin-amd64 && tar -czf ../$(BINARY_NAME)-$(VERSION)-darwin-amd64.tar.gz *
	@cd $(DIST_DIR)/darwin-arm64 && tar -czf ../$(BINARY_NAME)-$(VERSION)-darwin-arm64.tar.gz *
	@cd $(DIST_DIR)/windows-amd64 && zip -r ../$(BINARY_NAME)-$(VERSION)-windows-amd64.zip *
	@echo "Release archives created in $(DIST_DIR)/"

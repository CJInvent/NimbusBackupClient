# Nimbus Backup - Unified Build System
# Builds both CLI and GUI applications

.PHONY: all cli gui clean test help install-deps

# Version from the console's wails.json (CI stamps the real one from
# RELEASE_LINE and the tags; this is the local default)
VERSION := $(shell grep '"productVersion"' console/wails.json | cut -d'"' -f4)

# Build directories
BUILD_DIR := dist
CLI_DIR := cmd
# The two programs the MSI ships (docs/V4-CONSOLE.md): the console, its own
# module, and the service.
CONSOLE_DIR := console
SERVICE_DIR := gui

# Binary names
CLI_DIR_BIN := proxmoxbackup-directory
CLI_MACHINE_BIN := proxmoxbackup-machine
CLI_NBD_BIN := proxmoxbackup-nbd
SERVICE_BIN := NimbusBackupSVC
GUI_BIN := NimbusBackup

# Go build flags (security hardening)
GO_FLAGS := -trimpath -buildmode=pie
LDFLAGS := -s -w -X main.version=$(VERSION) \
	-extldflags '-static-pie -Wl,-z,relro,-z,now'

# Default target
all: cli gui service

help:
	@echo "Nimbus Backup Build System"
	@echo "=========================="
	@echo ""
	@echo "Targets:"
	@echo "  all          - Build everything (CLI + GUI + Service)"
	@echo "  cli          - Build all CLI tools"
	@echo "  gui          - Build the console (NimbusBackup.exe, console/)"
	@echo "  service      - Build the service (NimbusBackupSVC.exe, gui/)"
	@echo "  test         - Run all tests"
	@echo "  lint         - The golangci-lint passes, at the version CI pins"
	@echo "  gates        - Every gate CI runs that can run on Linux"
	@echo "  clean        - Remove build artifacts"
	@echo "  install-deps - Install build dependencies"
	@echo ""
	@echo "CLI Tools:"
	@echo "  cli-directory - Directory backup CLI"
	@echo "  cli-machine   - Machine backup CLI"
	@echo "  cli-nbd       - NBD server CLI"
	@echo ""
	@echo "Version: $(VERSION)"

# Install build dependencies
install-deps:
	@echo "📦 Installing dependencies..."
	go install github.com/wailsapp/wails/v2/cmd/wails@latest
	cd $(CONSOLE_DIR)/frontend && npm install

# CLI Builds
# NBD is Linux-only (uses Linux ioctl)
ifeq ($(shell go env GOOS),linux)
cli: cli-directory cli-machine cli-nbd
	@echo "✅ All CLI tools built successfully"
else
cli: cli-directory cli-machine
	@echo "✅ CLI tools built successfully (NBD skipped - Linux only)"
endif

cli-directory:
	@echo "🔨 Building Directory Backup CLI..."
	@mkdir -p $(BUILD_DIR)
	cd directorybackup && go mod tidy && go build $(GO_FLAGS) -ldflags="$(LDFLAGS)" \
		-o ../$(BUILD_DIR)/$(CLI_DIR_BIN)$(shell go env GOEXE)
	@echo "✅ Built: $(BUILD_DIR)/$(CLI_DIR_BIN)"

cli-machine:
	@echo "🔨 Building Machine Backup CLI..."
	@mkdir -p $(BUILD_DIR)
	cd machinebackup && go mod tidy && go build $(GO_FLAGS) -ldflags="$(LDFLAGS)" \
		-o ../$(BUILD_DIR)/$(CLI_MACHINE_BIN)$(shell go env GOEXE)
	@echo "✅ Built: $(BUILD_DIR)/$(CLI_MACHINE_BIN)"

cli-nbd:
ifeq ($(shell go env GOOS),linux)
	@echo "🔨 Building NBD Server CLI..."
	@mkdir -p $(BUILD_DIR)
	cd nbd && go mod tidy && go build $(GO_FLAGS) -ldflags="$(LDFLAGS)" \
		-o ../$(BUILD_DIR)/$(CLI_NBD_BIN)$(shell go env GOEXE)
	@echo "✅ Built: $(BUILD_DIR)/$(CLI_NBD_BIN)"
else
	@echo "⏭️  Skipping NBD Server CLI (Linux only)"
endif

# Service Build: gui/ is the service (no build tag since V4-CONSOLE C3)
service:
	@echo "🔧 Building the service..."
	@mkdir -p $(BUILD_DIR) $(SERVICE_DIR)/build/bin
	cd $(SERVICE_DIR) && go build -trimpath -ldflags="-s -w -X main.appVersion=$(VERSION)" \
		-o build/bin/$(SERVICE_BIN)$(shell go env GOEXE)
	@cp $(SERVICE_DIR)/build/bin/$(SERVICE_BIN)$(shell go env GOEXE) $(BUILD_DIR)/
	@echo "✅ Built: $(SERVICE_DIR)/build/bin/$(SERVICE_BIN) (for MSI)"

# Console Build (the window and the tray, a client of the service)
gui:
	@echo "🎨 Building the console (version $(VERSION))..."
	cd $(CONSOLE_DIR) && wails build -clean -platform $(shell go env GOOS)/$(shell go env GOARCH) \
		-ldflags "-X main.appVersion=$(VERSION)"
	@mkdir -p $(BUILD_DIR)
	@cp $(CONSOLE_DIR)/build/bin/$(GUI_BIN)$(shell go env GOEXE) $(BUILD_DIR)/
	@echo "✅ Built: $(BUILD_DIR)/$(GUI_BIN)"

# Console development mode
gui-dev:
	@echo "🚀 Starting the console in development mode..."
	cd $(CONSOLE_DIR) && wails dev

# Tests
test:
	@echo "🧪 Running tests..."
	go test -v -race -coverprofile=coverage.out ./...
	@echo "📊 Coverage report:"
	go tool cover -func=coverage.out

test-coverage:
	go tool cover -html=coverage.out -o coverage.html
	@echo "📊 Coverage report generated: coverage.html"

# Security checks
security-check:
	@echo "🔒 Running security checks..."
	@which gosec || go install github.com/securego/gosec/v2/cmd/gosec@latest
	gosec -severity high -confidence high ./...

# Linting — the golangci-lint passes CI runs, at the version CI pins.
#
# This used to be `golangci-lint run ./...` from the repo root with an
# @latest install, which answered a different question than CI does three
# ways over: a floating tool version, one pass instead of two (the service
# build was never linted here), and the workspace root instead of gui/ with
# GOWORK=off. A local lint that disagrees with the gate is worse than none,
# because it is trusted.
lint:
	@scripts/local-gates.sh lint

# Everything CI can run on Linux: gofmt, the three compile views, the lint
# passes, gosec, and every module's tests. See the script's header for what
# it deliberately does not cover.
gates:
	@scripts/local-gates.sh

# Clean build artifacts
clean:
	@echo "🧹 Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
	# build/bin only: build/windows/icon.ico and frontend/dist are tracked.
	rm -rf $(CONSOLE_DIR)/build/bin $(SERVICE_DIR)/build/bin
	rm -f coverage.out coverage.html
	@echo "✅ Clean complete"

# Cross-compile for all platforms
cross-compile: cross-cli-windows cross-cli-linux cross-cli-macos cross-gui-windows
	@echo "✅ All cross-compilation complete"

cross-cli-windows:
	@echo "🪟 Cross-compiling CLI for Windows..."
	@mkdir -p $(BUILD_DIR)/windows
	GOOS=windows GOARCH=amd64 cd directorybackup && go build $(GO_FLAGS) -ldflags="$(LDFLAGS)" \
		-o ../$(BUILD_DIR)/windows/$(CLI_DIR_BIN).exe
	GOOS=windows GOARCH=amd64 cd machinebackup && go build $(GO_FLAGS) -ldflags="$(LDFLAGS)" \
		-o ../$(BUILD_DIR)/windows/$(CLI_MACHINE_BIN).exe

cross-cli-linux:
	@echo "🐧 Cross-compiling CLI for Linux..."
	@mkdir -p $(BUILD_DIR)/linux
	GOOS=linux GOARCH=amd64 cd directorybackup && go build $(GO_FLAGS) -ldflags="$(LDFLAGS)" \
		-o ../$(BUILD_DIR)/linux/$(CLI_DIR_BIN)
	GOOS=linux GOARCH=amd64 cd machinebackup && go build $(GO_FLAGS) -ldflags="$(LDFLAGS)" \
		-o ../$(BUILD_DIR)/linux/$(CLI_MACHINE_BIN)

cross-cli-macos:
	@echo "🍎 Cross-compiling CLI for macOS..."
	@mkdir -p $(BUILD_DIR)/macos
	GOOS=darwin GOARCH=amd64 cd directorybackup && go build $(GO_FLAGS) -ldflags="$(LDFLAGS)" \
		-o ../$(BUILD_DIR)/macos/$(CLI_DIR_BIN)
	GOOS=darwin GOARCH=arm64 cd directorybackup && go build $(GO_FLAGS) -ldflags="$(LDFLAGS)" \
		-o ../$(BUILD_DIR)/macos/$(CLI_DIR_BIN)-arm64

cross-gui-windows:
	@echo "🪟🎨 Cross-compiling GUI for Windows..."
	cd $(CONSOLE_DIR) && wails build -clean -platform windows/amd64
	@mkdir -p $(BUILD_DIR)/windows
	@cp $(CONSOLE_DIR)/build/bin/$(GUI_BIN).exe $(BUILD_DIR)/windows/

# Release preparation
release: clean security-check lint test cross-compile
	@echo "📦 Preparing release v$(VERSION)..."
	@mkdir -p $(BUILD_DIR)/release
	cd $(BUILD_DIR) && tar -czf release/nimbus-backup-cli-v$(VERSION)-linux.tar.gz linux/
	cd $(BUILD_DIR) && zip -r release/nimbus-backup-cli-v$(VERSION)-windows.zip windows/*.exe
	cd $(BUILD_DIR) && tar -czf release/nimbus-backup-cli-v$(VERSION)-macos.tar.gz macos/
	cd $(BUILD_DIR) && zip -r release/NimbusBackup-v$(VERSION)-windows.zip windows/$(GUI_BIN).exe
	@echo "✅ Release packages ready in $(BUILD_DIR)/release/"
	@ls -lh $(BUILD_DIR)/release/

# Development setup
dev-setup: install-deps
	@echo "🔧 Setting up development environment..."
	go mod download
	cd $(CONSOLE_DIR)/frontend && npm install
	@echo "✅ Development environment ready"

.PHONY: gui-dev cross-compile cross-cli-windows cross-cli-linux cross-cli-macos cross-gui-windows release dev-setup security-check lint gates test-coverage

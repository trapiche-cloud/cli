# ── config ────────────────────────────────────────────────────────────────────
GITHUB_REPO  ?= trapiche-cloud/cli
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "v0.1.0")
BIN          := trapiche
BUILD_DIR    := dist
LDFLAGS      := -s -w -X github.com/trapiche/cli/cmd.Version=$(VERSION)
INSTALL_DIR  ?= /usr/local/bin

# MGC bucket/path where install.sh is publicly served
# Assumes trapiche.cloud/install.sh is routed to this bucket object
MGC_BUCKET   ?= trapiche-cdn
MGC_REGION   ?= br-se1

# ── targets ───────────────────────────────────────────────────────────────────
.PHONY: build install build-all release upload-install clean

## build: build for current platform
build:
	go build -ldflags="$(LDFLAGS)" -o $(BIN) .

## install: build and replace the installed CLI (asks for sudo)
install: build
	sudo install -m 0755 $(BIN) $(INSTALL_DIR)/$(BIN)
	@echo "Installed $(INSTALL_DIR)/$(BIN)"

## build-all: cross-compile for all supported platforms
build-all:
	mkdir -p $(BUILD_DIR)
	GOOS=linux   GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN)_linux_amd64  .
	GOOS=linux   GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN)_linux_arm64  .
	GOOS=darwin  GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN)_darwin_amd64 .
	GOOS=darwin  GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN)_darwin_arm64 .
	@echo "Built:"
	@ls -lh $(BUILD_DIR)/

## release: build all, create GitHub release, upload install.sh to MGC
release: build-all upload-install
	gh release create $(VERSION) \
		$(BUILD_DIR)/$(BIN)_linux_amd64 \
		$(BUILD_DIR)/$(BIN)_linux_arm64 \
		$(BUILD_DIR)/$(BIN)_darwin_amd64 \
		$(BUILD_DIR)/$(BIN)_darwin_arm64 \
		--repo $(GITHUB_REPO) \
		--title "$(VERSION)" \
		--notes ""
	@echo ""
	@echo "Released $(VERSION)"
	@echo "Install with:"
	@echo "  curl -fsSL https://trapiche.cloud/install.sh | bash"

## upload-install: push install.sh to MGC bucket
upload-install:
	mgc object-storage objects upload \
		--src install.sh \
		--dst $(MGC_BUCKET)/install.sh \
		--region $(MGC_REGION)
	@echo "install.sh uploaded to $(MGC_BUCKET)/install.sh"

## clean: remove build artifacts
clean:
	rm -rf $(BUILD_DIR) $(BIN)

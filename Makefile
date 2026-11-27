APP_NAME := alert-webhook
TARGET_OS ?= $(shell go env GOOS)
TARGET_ARCH ?= $(shell go env GOARCH)
DIST_DIR := dist
PKG_NAME := $(APP_NAME)-$(TARGET_OS)-$(TARGET_ARCH)
PKG_DIR := $(DIST_DIR)/$(PKG_NAME)
BIN_EXT := $(if $(filter windows,$(TARGET_OS)),.exe,)

.PHONY: build run test test-race vet package package-linux package-linux-arm64 package-all docker-build docker-up docker-down docker-logs clean

build:
	CGO_ENABLED=0 GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) go build -trimpath -ldflags="-s -w" -o $(APP_NAME)$(BIN_EXT) .

run:
	go run .

vet:
	go vet ./...

test:
	go test -v ./...

test-race:
	go test -race -v ./...

package:
	@echo "Packaging $(PKG_NAME)..."
	rm -rf $(PKG_DIR) $(DIST_DIR)/$(PKG_NAME).tar.gz
	mkdir -p $(PKG_DIR)
	CGO_ENABLED=0 GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) go build -trimpath -ldflags="-s -w" -o $(PKG_DIR)/$(APP_NAME)$(BIN_EXT) .
	cp scripts/start.sh $(PKG_DIR)/start.sh
	cp scripts/stop.sh $(PKG_DIR)/stop.sh
	cp config-example.yaml $(PKG_DIR)/config-example.yaml
	cp config-kafka-example.yaml $(PKG_DIR)/config-kafka-example.yaml
	cp .env.example $(PKG_DIR)/.env.example
	chmod +x $(PKG_DIR)/start.sh $(PKG_DIR)/stop.sh $(PKG_DIR)/$(APP_NAME)$(BIN_EXT)
	tar -czf $(DIST_DIR)/$(PKG_NAME).tar.gz -C $(DIST_DIR) $(PKG_NAME)
	@echo ""
	@echo "Package ready:"
	@echo "  Dir    : $(PKG_DIR)/"
	@echo "  Archive: $(DIST_DIR)/$(PKG_NAME).tar.gz"
	@echo "  Run    : cd $(PKG_DIR) && cp .env.example .env && ./start.sh"

package-linux:
	$(MAKE) package TARGET_OS=linux TARGET_ARCH=amd64

package-linux-arm64:
	$(MAKE) package TARGET_OS=linux TARGET_ARCH=arm64

package-all: package-linux package-linux-arm64

docker-build:
	docker compose build

docker-up:
	mkdir -p conf data logs
	docker compose up -d --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

clean:
	rm -rf $(APP_NAME) $(APP_NAME).exe $(DIST_DIR)

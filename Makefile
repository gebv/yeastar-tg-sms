.PHONY: build build-bot install install-bot test test-all clean playground

BINARY=yeastar-tg-sms
BOT_BINARY=yeastar-tg-bot
BUILD_DIR=./bin

# Version is derived from git tags; falls back to "dev" for local builds.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags="-s -w -X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/yeastar-tg-sms

build-bot:
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BOT_BINARY) ./cmd/yeastar-tg-bot

install: build
	cp $(BUILD_DIR)/$(BINARY) /usr/local/bin/

install-bot: build-bot
	cp $(BUILD_DIR)/$(BOT_BINARY) /usr/local/bin/

test:
	go test ./api/ -v -count=1 -run 'Test[^P]'

test-all:
	go test ./... -v -count=1

clean:
	rm -rf $(BUILD_DIR)

playground:
	go test -timeout 120s -run ^Test_Playground$$ github.com/gebv/yeastar-tg-sms/api -v -failfast -count=1

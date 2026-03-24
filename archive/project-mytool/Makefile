# tent Makefile

PREFIX ?= /usr/local/bin

.PHONY: all build sign install uninstall test lint clean vet

all: build sign

build:
	go build -o tent ./cmd/tent
	go build -o tent-agent ./cmd/tent-agent

sign: build
	codesign --force --sign - --entitlements tent.entitlements tent
	codesign --force --sign - --entitlements tent.entitlements tent-agent

install: sign
	@echo "Installing tent to $(PREFIX)... (may require sudo)"
	sudo install -d $(PREFIX)
	sudo install -m 755 tent $(PREFIX)/tent
	sudo install -m 755 tent-agent $(PREFIX)/tent-agent
	@echo "Done. Run 'tent --help' to get started."

uninstall:
	sudo rm -f $(PREFIX)/tent $(PREFIX)/tent-agent

test:
	go test ./... -v -count=1

lint:
	go vet ./...

clean:
	rm -f tent tent-agent

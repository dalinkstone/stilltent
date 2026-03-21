# tent Makefile

.PHONY: all build test lint clean vet

all: build

build:
	go build -o tent ./cmd/tent

test:
	go test ./... -v -count=1

lint:
	go vet ./...

clean:
	rm -f tent

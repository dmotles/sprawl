.PHONY: build test clean install

build:
	go build -o dendra .

test:
	go test ./...

GOBIN ?= $(HOME)/.local/bin

install:
	GOBIN=$(GOBIN) go install .

clean:
	rm -f dendra

.PHONY: build test clean install

build:
	go build -o dendra .

test:
	go test ./...

install:
	go install .

clean:
	rm -f dendra

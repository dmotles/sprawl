.PHONY: build test clean

build:
	go build -o dendra .

test:
	go test ./...

clean:
	rm -f dendra

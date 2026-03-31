.PHONY: build test clean install

build:
	go build -o dendra .

test:
	go test ./...

install: build
	sudo cp ./dendra /usr/local/bin/dendra

clean:
	rm -f dendra

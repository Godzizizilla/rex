PREFIX ?= /usr/local/bin

.PHONY: build install clean

build:
	go build -o bin/rex ./cmd/rex
	go build -o bin/rexd ./cmd/rexd

install: build
	cp bin/rex $(PREFIX)/rex
	cp bin/rexd $(PREFIX)/rexd

clean:
	rm -rf bin/ rex rexd

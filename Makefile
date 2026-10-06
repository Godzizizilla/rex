PREFIX ?= /usr/local/bin
APPDIR ?= /Applications

.PHONY: build test app install install-app clean

# CLI binaries: bin/rex, bin/rexd
build:
	go build -o bin/rex ./cmd/rex
	go build -o bin/rexd ./cmd/rexd

test:
	go vet ./...
	go test -race ./...

# macOS app with rexd and rex bundled: build/Rex.app
app:
	./scripts/build-app.sh

# Install the rex CLI (used by agents) and rexd.
install: build
	cp bin/rex $(PREFIX)/rex
	cp bin/rexd $(PREFIX)/rexd

# Install Rex.app into /Applications.
install-app: app
	rm -rf "$(APPDIR)/Rex.app"
	cp -R build/Rex.app "$(APPDIR)/Rex.app"

clean:
	rm -rf bin/ build/ app/.build

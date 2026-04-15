VERSION ?= dev
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
BINARY := goback

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/goback

test:
	go test -v ./...

clean:
	rm -rf dist/
	rm -f $(BINARY)

release: clean test
	@mkdir -p dist
	cp goback.1 dist/
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY) ./cmd/goback && \
		tar -czf dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz -C dist $(BINARY) goback.1 && rm dist/$(BINARY)
	GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY) ./cmd/goback && \
		tar -czf dist/$(BINARY)-$(VERSION)-linux-arm64.tar.gz -C dist $(BINARY) goback.1 && rm dist/$(BINARY)
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY) ./cmd/goback && \
		tar -czf dist/$(BINARY)-$(VERSION)-darwin-amd64.tar.gz -C dist $(BINARY) goback.1 && rm dist/$(BINARY)
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY) ./cmd/goback && \
		tar -czf dist/$(BINARY)-$(VERSION)-darwin-arm64.tar.gz -C dist $(BINARY) goback.1 && rm dist/$(BINARY)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY).exe ./cmd/goback && \
		cd dist && zip $(BINARY)-$(VERSION)-windows-amd64.zip $(BINARY).exe goback.1 && rm $(BINARY).exe
	rm dist/goback.1

deploy: build install-man install-completion
	# Unload the daemon BEFORE overwriting the binary. Copying over a
	# running ad-hoc-signed executable on Apple Silicon invalidates the
	# in-kernel code-signing state and subsequent launches are killed
	# with OS_REASON_CODESIGNING.
	-launchctl unload ~/Library/LaunchAgents/com.goback.daemon.plist 2>/dev/null
	cp $(BINARY) /usr/local/bin/
	codesign --force --sign - /usr/local/bin/$(BINARY)
	launchctl load ~/Library/LaunchAgents/com.goback.daemon.plist

install-man:
	install -d /usr/local/share/man/man1
	install -m 644 goback.1 /usr/local/share/man/man1/goback.1

install-completion: build
	install -d ~/.oh-my-zsh/custom/completions
	./$(BINARY) completion zsh > ~/.oh-my-zsh/custom/completions/_goback

reauth-keychain:
	./scripts/reauth-keychain.sh

.PHONY: build test clean release deploy install-man install-completion reauth-keychain

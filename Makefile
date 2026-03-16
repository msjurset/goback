VERSION ?= dev
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
BINARY := markback

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/markback

test:
	go test -v ./...

clean:
	rm -rf dist/
	rm -f $(BINARY)

release: clean test
	@mkdir -p dist
	cp markback.1 dist/
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY) ./cmd/markback && \
		tar -czf dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz -C dist $(BINARY) markback.1 && rm dist/$(BINARY)
	GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY) ./cmd/markback && \
		tar -czf dist/$(BINARY)-$(VERSION)-linux-arm64.tar.gz -C dist $(BINARY) markback.1 && rm dist/$(BINARY)
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY) ./cmd/markback && \
		tar -czf dist/$(BINARY)-$(VERSION)-darwin-amd64.tar.gz -C dist $(BINARY) markback.1 && rm dist/$(BINARY)
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY) ./cmd/markback && \
		tar -czf dist/$(BINARY)-$(VERSION)-darwin-arm64.tar.gz -C dist $(BINARY) markback.1 && rm dist/$(BINARY)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY).exe ./cmd/markback && \
		cd dist && zip $(BINARY)-$(VERSION)-windows-amd64.zip $(BINARY).exe markback.1 && rm $(BINARY).exe
	rm dist/markback.1

deploy: build install-man install-completion
	cp $(BINARY) /usr/local/bin/
	-launchctl unload ~/Library/LaunchAgents/com.markback.daemon.plist 2>/dev/null
	launchctl load ~/Library/LaunchAgents/com.markback.daemon.plist

install-man:
	install -d /usr/local/share/man/man1
	install -m 644 markback.1 /usr/local/share/man/man1/markback.1

install-completion:
	install -d ~/.oh-my-zsh/custom/completions
	install -m 644 _markback ~/.oh-my-zsh/custom/completions/_markback

.PHONY: build test clean release deploy install-man install-completion

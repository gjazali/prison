GUEST_GOOS ?= linux
GUEST_GOARCH ?= arm64
VERSION ?= $(shell date +%Y%m%d)-dev
LDFLAGS := -s -w -X prison/internal/cli.Version=$(VERSION)

.PHONY: all build guest host test test-guest test-broker lint install clean

all: build

build: guest host

guest:
	GOOS=$(GUEST_GOOS) GOARCH=$(GUEST_GOARCH) CGO_ENABLED=0 \
		go build -trimpath -ldflags '$(LDFLAGS)' \
		-o images/base/prison-guest ./cmd/prison-guest

host:
	mkdir -p bin
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/prison ./cmd/prison

test:
	go test ./...

test-guest: guest
	go test -tags integration ./internal/guest/... -run Integration -v

test-broker:
	go test -tags integration ./internal/broker/... -run Integration -v

lint:
	@test -z "$$(gofmt -l . | grep -v '^legacy/' | grep -v '^spikes/')" \
		|| (gofmt -l . | grep -v '^legacy/' | grep -v '^spikes/'; exit 1)
	go vet ./...
	@command -v staticcheck >/dev/null && staticcheck ./... || true

install: build
	install -m 0755 bin/prison /usr/local/bin/prison

clean:
	rm -rf bin images/base/prison-guest

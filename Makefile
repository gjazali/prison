HOST_MACHINE := $(shell uname -m)
HOST_GOARCH := $(subst aarch64,arm64,$(subst x86_64,amd64,$(HOST_MACHINE)))
GUEST_GOOS ?= linux
GUEST_GOARCH ?= $(HOST_GOARCH)
KERNEL := images/kernel/linux-$(GUEST_GOARCH).gz
VERSION ?= $(shell date +%Y%m%d)-dev
LDFLAGS := -s -w -X prison/internal/cli.Version=$(VERSION)

.PHONY: all build guest host kernel test test-guest test-broker test-cage \
	lint install clean

all: build

build: guest host

guest:
	GOOS=$(GUEST_GOOS) GOARCH=$(GUEST_GOARCH) CGO_ENABLED=0 \
		go build -trimpath -ldflags '$(LDFLAGS)' \
		-o images/base/prison-guest ./cmd/prison-guest

# The Linux build embeds the aws-firecracker guest kernel.
ifeq ($(shell uname -s),Linux)
host: $(KERNEL)
endif

host:
	mkdir -p bin
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/prison ./cmd/prison

$(KERNEL):
	kernel/build.sh images/kernel

kernel:
	kernel/build.sh images/kernel

test:
	go test ./...

test-guest: guest
	go test -tags integration ./internal/guest/... -run Integration -v

test-broker:
	go test -tags integration ./internal/broker/... -run Integration -v

# test-cage boots microVMs. It needs Linux with KVM, sudo, and the tools that
# `prison doctor` lists for the aws-firecracker cage.
test-cage: build
	go test -tags integration ./internal/cage/firecracker/... \
		./internal/hostfw/... -run Integration -v

lint:
	@test -z "$$(gofmt -l . | grep -v '^legacy/' | grep -v '^spikes/')" \
		|| (gofmt -l . | grep -v '^legacy/' | grep -v '^spikes/'; exit 1)
	go vet ./...
	@command -v staticcheck >/dev/null && staticcheck ./... || true

install: build
	install -m 0755 bin/prison /usr/local/bin/prison

clean:
	rm -rf bin images/base/prison-guest images/kernel/*.gz \
		images/kernel/version

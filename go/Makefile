VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
OUT     := dist

PLATFORMS := \
	linux/amd64 \
	linux/arm64 \
	linux/arm \
	darwin/amd64 \
	darwin/arm64 \
	windows/amd64 \
	windows/arm64 \
	freebsd/amd64

.PHONY: all test race build release clean vet

all: test build

test:
	go test ./...

race:
	go test -race -count=1 ./...

vet:
	go vet ./...

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(OUT)/fyrwire ./cmd/fyrwire

release: clean
	@mkdir -p $(OUT)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "  $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -ldflags "$(LDFLAGS)" \
			-o $(OUT)/fyrwire-$$os-$$arch$$ext ./cmd/fyrwire || exit 1; \
	done
	@cd $(OUT) && sha256sum fyrwire-* > SHA256SUMS
	@echo
	@ls -1sh $(OUT)

clean:
	rm -rf $(OUT)

BINARY  := mesh-voice
GOFLAGS := -trimpath -ldflags="-s -w"

.PHONY: build build-arm64 clean

build:
	go build $(GOFLAGS) -o bin/$(BINARY) .

build-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=1 \
	CC=aarch64-linux-gnu-gcc \
	go build $(GOFLAGS) -o bin/$(BINARY)-linux-arm64 .

clean:
	rm -rf bin/

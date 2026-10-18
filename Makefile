.DEFAULT_GOAL := help
.PHONY: help check fmt vet test build install run clean

help: ## show this help
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}'

check: fmt vet test ## format, vet and test: run before a change is done

fmt: ## format all Go files
	gofmt -w .

vet: ## run go vet
	go vet ./...

test: ## run the unit tests
	go test ./...

build: ## build ./bin/rig
	go build -o bin/rig ./cmd/rig

install: ## install rig to ~/go/bin
	go install ./cmd/rig

run: ## start the TUI from source
	go run ./cmd/rig

clean: ## remove ./bin
	rm -rf bin

.PHONY: check test bench

check:
	gofmt -l . | grep . && exit 1 || true
	go mod tidy
	go vet ./...

test:
	go test -timeout 120s -race -cover ./...

bench:
	go test -timeout 120s -bench=. -benchmem ./...
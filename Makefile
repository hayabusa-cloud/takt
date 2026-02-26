.PHONY: check test bench

check:
	gofmt -l . | grep . && exit 1 || true
	go mod tidy
	go vet ./...

test:
	go test -race -cover ./...

bench:
	go test -bench=. -benchmem ./...
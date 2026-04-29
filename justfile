default: check

check: tidy vet lint test

tidy:
    go mod tidy

vet:
    go vet ./...

lint:
    golangci-lint run ./...

test:
    go test ./...

test-race:
    go test -race ./...

build:
    go build ./...

cover:
    go test -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out

smoke:
    go test -tags=smoke -run Smoke -v ./...

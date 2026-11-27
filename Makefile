.PHONY: build test vet fmt cover integration clean

build:
	go build ./...

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

integration:
	go test -tags=integration -count=1 ./...

clean:
	rm -f coverage.out

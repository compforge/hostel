.PHONY: fix lint test

fix:
	gofmt -w .

lint:
	@test -z "$$(gofmt -l .)"
	go vet ./...

test:
	go test -race -count=1 ./...

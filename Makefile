.PHONY: build test dev clean

build:
	go build -o bin/statusgrid-server ./cmd/statusgrid-server
	go build -o bin/statusgrid-probe ./cmd/statusgrid-probe

test:
	go vet ./...
	go test -race -v ./...

dev: build
	./bin/statusgrid-server

clean:
	rm -rf bin/ coverage.out

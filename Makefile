.PHONY: run run-worker test build build-worker deploy lint clean

BINARY        := tuipod-api
WORKER_BINARY := tuipod-worker
CMD           := ./cmd/server
WORKER_CMD    := ./cmd/worker
IMAGE         := tuipod-api

run:
	go run $(CMD)/main.go

# Podcast feed refresher — run as a separate process, 1 instance only.
run-worker:
	go run $(WORKER_CMD)/main.go

build:
	go build -ldflags="-s -w" -o bin/$(BINARY) $(CMD)/main.go

build-worker:
	go build -ldflags="-s -w" -o bin/$(WORKER_BINARY) $(WORKER_CMD)/main.go

test:
	go test ./... -count=1 -race -p 1

lint:
	go vet ./...

clean:
	rm -rf bin/

deploy:
	docker compose build api worker
	docker compose up -d

APP_NAME := alert-webhook

.PHONY: build run test test-race docker-build docker-up docker-down docker-logs clean

build:
	go build -trimpath -ldflags="-s -w" -o $(APP_NAME) .

run:
	go run .

test:
	go test -v ./...

test-race:
	go test -race -v ./...

docker-build:
	docker compose build

docker-up:
	mkdir -p conf data logs
	docker compose up -d --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

clean:
	rm -f $(APP_NAME) $(APP_NAME).exe

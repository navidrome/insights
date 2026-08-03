dev: build
	@if [ ! -f ".env" ]; then echo "PORT=8080" > .env; fi
	docker compose up
.PHONY: dev

build:
	docker compose build
.PHONY: build

lint:
	@docker compose run --rm app golangci-lint run ./...
.PHONY: lint

linux:
	docker buildx build --platform linux/amd64 --target binary --output binary -f docker/app-prod/Dockerfile .
.PHONY: linux

summarize:
	@if [ -z "$(DATA)" ]; then \
		echo "Usage: make summarize DATA=<data-folder> [DAYS=5]"; \
		exit 1; \
	fi
	DATA_FOLDER="$(DATA)" go run ./cmd/process -once -days $(or $(DAYS),5)
.PHONY: summarize

monitor:
	@if [ -z "$(DATA)" ]; then \
		echo "Usage: make monitor DATA=<data-folder> [DATE=YYYY-MM-DD]"; \
		exit 1; \
	fi
	go run ./cmd/monitor -data "$(DATA)" $(if $(DATE),-date $(DATE),)
.PHONY: monitor

test:
	go test ./...
.PHONY: test

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
	docker buildx build --platform linux/amd64 --target binary --output binary docker/app-prod
.PHONY: linux

consolidate:
	@if [ -z "$(BACKUPS)" ] || [ -z "$(DEST)" ]; then \
		echo "Usage: make consolidate BACKUPS=<path-to-backups> DEST=<destination-folder>"; \
		exit 1; \
	fi
	go run ./cmd/consolidate -backups "$(BACKUPS)" -dest "$(DEST)"
.PHONY: consolidate

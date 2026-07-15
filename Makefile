# List all targets with descriptions:  make help
MODULE := workflow-engine
CMD     := ./cmd/engine
BINARY  := engine
BIN_DIR := ./bin
GO      := go
LDFLAGS := -ldflags="-s -w"
TEST_FLAGS := -count=1
COVER_OUT  := coverage.out
COVER_HTML := coverage.html
IMAGE_TAG  := workflow-engine:latest
COMPOSE_PROJECT := workflow-engine

ifeq ($(OS),Windows_NT)
    MKDIR_BIN := powershell -NoProfile -Command "New-Item -ItemType Directory -Force -Path $(BIN_DIR) | Out-Null"
    RM_RF     := powershell -NoProfile -Command "Remove-Item -Recurse -Force -ErrorAction SilentlyContinue '$(BIN_DIR)', '$(COVER_OUT)', '$(COVER_HTML)', 'data'; exit 0"
    DEMO_UP_CMD := powershell -NoProfile -Command "$$env:DEMO_TASK_DELAY='1s'; docker compose -p $(COMPOSE_PROJECT) up --build -d"
else
    MKDIR_BIN := mkdir -p $(BIN_DIR)
    RM_RF     := rm -rf $(BIN_DIR) $(COVER_OUT) $(COVER_HTML) data/
    DEMO_UP_CMD := DEMO_TASK_DELAY=1s docker compose -p $(COMPOSE_PROJECT) up --build -d
endif

.PHONY: all build run demo demo-up test test-race test-race-docker test-cover cover-html \
        fmt vet check tidy \
        docker-build docker-up docker-down docker-logs docker-ps \
        clean help

## all: Check and build project binary
all: check build

## build: Compile stripped release binary
build:
	@echo "==> Building $(BINARY)…"
	@$(MKDIR_BIN)
	$(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) $(CMD)
	@echo "    Binary: $(BIN_DIR)/$(BINARY)"

## build-debug: Compile retaining debug symbols
build-debug:
	@echo "==> Building $(BINARY) [debug]…"
	@$(MKDIR_BIN)
	$(GO) build -o $(BIN_DIR)/$(BINARY)-debug $(CMD)

## run: Build and run with default settings (local sqlite DB in data/)
run: build
	@echo "==> Running $(BINARY)…"
	$(BIN_DIR)/$(BINARY)

## test: Run unit tests
test:
	@echo "==> Running tests…"
	$(GO) test $(TEST_FLAGS) ./...

## test-race: Run tests with race detector
test-race:
	@echo "==> Running tests with race detector…"
	$(GO) test $(TEST_FLAGS) -race ./...

## test-race-docker: Run tests with race detector inside a Go Docker container (no local GCC/CGO needed)
test-race-docker:
	@echo "==> Running tests with race detector inside Docker container…"
	docker run --rm -v "$(CURDIR):/app" -w /app golang:1.25 go test $(TEST_FLAGS) -race ./...

## test-cover: Run tests with coverage profiling
test-cover:
	@echo "==> Running tests with coverage…"
	$(GO) test $(TEST_FLAGS) -coverprofile=$(COVER_OUT) -covermode=atomic ./...
	$(GO) tool cover -func=$(COVER_OUT)

## cover-html: Run tests and generate HTML coverage report
cover-html: test-cover
	@echo "==> Generating HTML coverage report…"
	$(GO) tool cover -html=$(COVER_OUT) -o $(COVER_HTML)
	@echo "    Report: $(COVER_HTML)"

## fmt: Format Go source code
fmt:
	@echo "==> Formatting…"
	$(GO) fmt ./...

## vet: Run go vet static analysis
vet:
	@echo "==> Running go vet…"
	$(GO) vet ./...

## check: Format and vet the codebase
check: fmt vet

## tidy: Clean up dependencies
tidy:
	@echo "==> Tidying module…"
	$(GO) mod tidy
	$(GO) mod verify

## docker-build: Build the docker image using the relocated Dockerfile
docker-build:
	@echo "==> Building Docker image $(IMAGE_TAG)…"
	docker build -t $(IMAGE_TAG) -f docker/Dockerfile .

## docker-up: Start docker-compose services in detached mode
docker-up:
	@echo "==> Starting Docker Compose services…"
	docker compose -p $(COMPOSE_PROJECT) up --build -d

## docker-down: Stop docker-compose services (preserves volumes)
docker-down:
	@echo "==> Stopping Docker Compose services…"
	docker compose -p $(COMPOSE_PROJECT) down

## docker-down-volumes: Stop docker-compose services and remove volumes
docker-down-volumes:
	@echo "==> Stopping Docker Compose services and removing volumes…"
	docker compose -p $(COMPOSE_PROJECT) down -v

## docker-logs: Follow logs from docker-compose services
docker-logs:
	docker compose -p $(COMPOSE_PROJECT) logs -f

## docker-ps: Show docker-compose service status
docker-ps:
	docker compose -p $(COMPOSE_PROJECT) ps

## clean: Remove build artifacts and temporary files
clean:
	@echo "==> Cleaning…"
	@$(RM_RF)
	@echo "    Done."

## help: Show help for Make targets
help:
	@$(GO) run scripts/help/main.go

## demo: Run an interactive API demonstration script
demo:
	@$(GO) run scripts/demo/main.go

demo-up:
	@echo "==> Starting Docker Compose with 1s task delay (slow-motion mode)..."
	@$(DEMO_UP_CMD)
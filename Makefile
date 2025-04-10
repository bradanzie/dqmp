# Makefile
.PHONY: all build dqmpd dqmpctl run clean test fmt tidy

# Variables
DQMPD_BINARY=dqmpd
DQMPCTL_BINARY=dqmpctl
DQMPD_CMD_PATH=./cmd/dqmpd
DQMPCTL_CMD_PATH=./cmd/dqmpctl
CONFIG_FILE?=config/default.yaml

all: build

build: dqmpd dqmpctl

dqmpd: tidy fmt
	@echo "Building ${DQMPD_BINARY}..."
	@go build -o bin/${DQMPD_BINARY} ${DQMPD_CMD_PATH}
	@echo "Build complete: bin/${DQMPD_BINARY}"

dqmpctl: tidy fmt
	@echo "Building ${DQMPCTL_BINARY}..."
	@go build -o bin/${DQMPCTL_BINARY} ${DQMPCTL_CMD_PATH}
	@echo "Build complete: bin/${DQMPCTL_BINARY}"

run: dqmpd # Assure que dqmpd est construit
	@echo "Running ${DQMPD_BINARY} with config ${CONFIG_FILE}..."
	@./bin/${DQMPD_BINARY} --config ${CONFIG_FILE}

test: tidy
	@echo "Running tests..."
	@go test ./...

clean:
	@echo "Cleaning build artifacts..."
	@rm -rf bin/

fmt:
	@echo "Formatting code..."
	@go fmt ./...

tidy:
	@echo "Tidying modules..."
	@go mod tidy
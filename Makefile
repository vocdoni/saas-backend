.PHONY: swagger test lint lint-strict

# Generate Swagger documentation
swagger:
	@echo "Generating Swagger documentation..."
	@./scripts/generate-swagger.sh

# Run tests
test:
	@echo "Running tests..."
	@go test -v ./...

# Run linter
lint:
	@echo "Running linter..."
	@golangci-lint run

# Run linter against the whole repo (no --new-from-rev scoping)
lint-strict:
	@echo "Running linter (strict, whole repo)..."
	@golangci-lint run

# Default target
all: test lint swagger

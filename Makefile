.PHONY: swagger test lint

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

# Default target
all: test lint swagger

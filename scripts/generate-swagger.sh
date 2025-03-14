#!/bin/bash

# Check if swag is installed, if not install it
if ! command -v swag &> /dev/null; then
    echo "swag not found, installing..."
    go install github.com/swaggo/swag/cmd/swag@latest
    [ $? -ne 0 ] && echo "Error: swag installation failed." && exit 1
    echo "swag installed successfully."
fi

# Create docs package directory if it doesn't exist
mkdir -p docs
rm -f docs/swagger.yaml

# Generate Swagger documentation from code comments
echo "Running swag..."
swag fmt -d ./
swag init -g api/api.go -o docs --outputTypes yaml --parseDependency --parseInternal --parseDepth=4

# Check if the swagger.yaml file was generated
if [ -f docs/swagger.yaml ]; then
    echo "Swagger documentation generated successfully at docs/swagger.yaml"
else
    echo "Error: swagger.yaml was not generated."
    exit 1
fi

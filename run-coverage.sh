#!/bin/bash

# Create coverage output directory
mkdir -p coverage

# Run tests for the provider with coverage
go test -coverprofile=coverage/provider.out \
  -coverpkg=github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws \
  ./test/unit/adapters/platform/aws/

# Display coverage stats
go tool cover -func=coverage/provider.out

# Generate HTML report
go tool cover -html=coverage/provider.out -o coverage/provider.html

echo "Coverage report generated at coverage/provider.html"

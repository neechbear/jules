.PHONY: build test clean

build:
	@echo "Building xnb_parse_cli..."
	@go build -o build/xnb_parse_cli ./xnb_parse_cli

test:
	@echo "Running tests..."
	@go test -v ./xnb_parse

clean:
	@echo "Cleaning up..."
	@rm -rf build

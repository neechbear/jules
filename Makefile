.PHONY: build test clean

build/xnb_parse_cli: $(wildcard xnb_parse_cli/*) $(wildcard xnb_parse/*)
	go build -o build/xnb_parse_cli ./xnb_parse_cli

build: build/xnb_parse_cli

test:
	go test -v ./xnb_parse

clean:
	rm -rf build

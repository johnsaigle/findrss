.PHONY: build install test clean

BINARY_NAME := findrss
BUILD_DIR := .
INSTALL_DIR := $(HOME)/.local/bin

build:
	go build -o $(BUILD_DIR)/$(BINARY_NAME) main.go

install: build
	@mkdir -p $(INSTALL_DIR)
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)

test:
	go test -v ./...

clean:
	@rm -f $(BUILD_DIR)/$(BINARY_NAME)

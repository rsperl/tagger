BINARY_NAME = tagger
BIN_DIR = bin
INSTALL_DIR = $(HOME)/bin

.PHONY: all build clean install

all: build

# Build the Go binary into ./bin
build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY_NAME) .

# Remove build artifacts
clean:
	rm -rf $(BIN_DIR)

# Install the binary to ~/bin
install: build
	mkdir -p $(INSTALL_DIR)
	cp $(BIN_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)
	@echo "Successfully installed $(BINARY_NAME) to $(INSTALL_DIR)"
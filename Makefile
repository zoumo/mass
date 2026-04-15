BIN_DIR := bin

.PHONY: build clean mass massctl

build: mass massctl

mass:
	go build -o $(BIN_DIR)/mass ./cmd/mass

massctl:
	go build -o $(BIN_DIR)/massctl ./cmd/massctl

clean:
	rm -rf $(BIN_DIR)

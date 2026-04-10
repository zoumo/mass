BIN_DIR := bin

.PHONY: build clean agentd agentdctl

build: agentd agentdctl

agentd:
	go build -o $(BIN_DIR)/agentd ./cmd/agentd

agentdctl:
	go build -o $(BIN_DIR)/agentdctl ./cmd/agentdctl

clean:
	rm -rf $(BIN_DIR)

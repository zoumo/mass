CMDS     := $(wildcard cmd/*)
BINARIES := $(notdir $(CMDS))
BIN_DIR  := bin

.PHONY: build clean $(BINARIES)

build: $(BINARIES)

$(BINARIES):
	go build -o $(BIN_DIR)/$@ ./cmd/$@

clean:
	rm -rf $(BIN_DIR)

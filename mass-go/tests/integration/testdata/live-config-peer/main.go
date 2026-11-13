// Synthetic stdio-to-owned-TCP relay. No Agent, model or personal data access.
package main

import (
	"encoding/json"
	"io"
	"net"
	"os"

	"github.com/zoumo/mass/mass-go/tests/integration/testdata/clipeer"
)

func main() {
	if clipeer.Version() {
		return
	}
	conn, err := net.Dial("tcp", os.Getenv("CONFIG_PEER_ADDR"))
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(conn).Encode(map[string]any{"peerOpen": true, "cwd": cwd, "args": os.Args[1:]}); err != nil {
		panic(err)
	}
	go func() { _, _ = io.Copy(conn, os.Stdin); _ = conn.(*net.TCPConn).CloseWrite() }()
	_, _ = io.Copy(os.Stdout, conn)
}

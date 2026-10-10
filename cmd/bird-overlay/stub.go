//go:build !desktop

// Command bird-overlay needs Wails build tags; see `make overlay`.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "bird-overlay was built without the desktop tag.\n"+
		"Build it with: make overlay   (go build -tags desktop,production,webkit2_41 ./cmd/bird-overlay)")
	os.Exit(1)
}

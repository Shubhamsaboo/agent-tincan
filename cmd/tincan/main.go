// Command tincan lets AI agents on one tailnet ask each other to do things.
// The same binary runs the relay and the per-agent client.
package main

import (
	"fmt"
	"os"
)

// Version is set at link time by the release build.
var Version = "0.0.1-dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(Version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: tincan <command>")
	os.Exit(2)
}

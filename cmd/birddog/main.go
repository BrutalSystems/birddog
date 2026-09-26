// Command birddog observes live coding-agent sessions on this machine.
//
// It reports what it sees and what meets the attention rules it was given. It
// never sends a prompt, approves a request, or starts, stops or restarts a
// watched session — and stopping birddog leaves every one of them running.
package main

import (
	"fmt"
	"os"

	"github.com/BrutalSystems/birddog/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}

var _ = fmt.Sprint

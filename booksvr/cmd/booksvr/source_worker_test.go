package main

import (
	"fmt"
	"os"
	"testing"
)

// CLI integration tests execute the same internal worker as a production binary.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "source-sync-worker" {
		if err := Run(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

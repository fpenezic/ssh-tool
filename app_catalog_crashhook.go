//go:build catalogcrashtest

package main

import (
	"log"
	"os"
)

// Crash-test builds only: exit right after the catalog accepted a publish
// and before this machine converts the rows, to prove the next sync
// finishes the conversion in place instead of creating copies.
func init() {
	catalogPublishAfterPost = func() {
		log.Printf("catalog: crash test - exiting after the publish was accepted")
		os.Exit(3)
	}
}

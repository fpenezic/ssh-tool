//go:build !ios

package main

// main lets `go build ./...` build this directory on desktop: the iOS
// entry point is WailsIOSMain in main_ios.go (ios tag), and without a main
// the package failed with "function main is undeclared".
func main() {}

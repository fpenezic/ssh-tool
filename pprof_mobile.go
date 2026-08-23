//go:build android || ios

package main

// startPprof is desktop-only: there is no way to reach a localhost port on
// a phone without also shipping the profiler's attack surface to every
// user, and the memory questions it answers are desktop ones.
func startPprof() {}

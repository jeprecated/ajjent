// Package buildcfg holds the settings a packager fixes at link time.
//
// They live outside package main because the Go linker addresses a variable by
// its package path, and cmd/ajj is "main" in the ajj binary but
// "github.com/jeprecated/ajjent/cmd/ajj" in its test binary. One -X flag for
// this package reaches both, so the tests validate exactly what the binary
// trusts.
package buildcfg

// NoCleanupJJVersion is the one Jujutsu release this build trusts for the
// machine create noCleanup ownership proof. Set it with
//
//	-ldflags "-X github.com/jeprecated/ajjent/internal/buildcfg.NoCleanupJJVersion=<x.y.z>"
//
// to the jj the build runs cmd/ajj's tests against; those tests are the
// validation. The default serves plain `go build` and `go test`: it is the
// release this project's CI installs.
var NoCleanupJJVersion = "0.43.0"

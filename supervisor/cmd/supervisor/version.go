package main

// version is the binary version string. It is overridden at build time via
// the linker flag -ldflags="-X main.version=<tag>" and defaults to "dev"
// for untagged local builds.
var version = "dev"

package main

// appVersion is injected at build time (-ldflags "-X main.appVersion=...",
// the CI version job's L.CC.RR). The service carries its own copy of the same
// stamp (gui/version.go); both are set from the same build step.
var appVersion = "dev"

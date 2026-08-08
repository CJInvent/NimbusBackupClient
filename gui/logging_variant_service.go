//go:build service
// +build service

package main

// logVariant — see logging_variant_gui.go. Yes, this produces
// "service-service.log": that is the name the service build has always
// written, and renaming it would orphan the logs on every installed machine
// and break the paths the GUI's viewer already knows.
const logVariant = "service"

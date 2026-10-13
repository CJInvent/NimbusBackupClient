//go:build !service
// +build !service

package main

// resetServiceDatapathState has nothing to clear in the console build: the
// run-bound and browse grants exist only in the service (datapath_service.go).
func resetServiceDatapathState() {}

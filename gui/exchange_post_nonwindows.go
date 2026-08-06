//go:build !windows && service
// +build !windows,service

package main

// Exchange post-backup tasks are Windows-only, and only the service runs
// backups. Both conditions, so both tags.
func runExchangePostBackup(version string, healthCheck, truncate bool) {}

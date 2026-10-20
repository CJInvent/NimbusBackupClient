//go:build !windows

package main

// ensureWebView2Runtime has nothing to install outside Windows: the console
// ships for Windows only.
func ensureWebView2Runtime() {}

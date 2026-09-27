//go:build windows

package main

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// F-44: pins the golang.org/x/sys contract Wails' single-instance lock (and
// any lock like it) depends on: the SECOND CreateMutex of a name reports
// ERROR_ALREADY_EXISTS as its returned error. The code this replaced ignored
// that error and asked GetLastError() again, which Go does not keep.
func TestSecondCreateMutexReturnsAlreadyExists(t *testing.T) {
	name, err := windows.UTF16PtrFromString(fmt.Sprintf("nimbus-test-mutex-%d", os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	first, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		t.Fatalf("first CreateMutex: %v", err)
	}
	defer windows.CloseHandle(first)
	second, err := windows.CreateMutex(nil, false, name)
	if second != 0 {
		defer windows.CloseHandle(second)
	}
	if err != windows.ERROR_ALREADY_EXISTS {
		t.Fatalf("second CreateMutex returned %v, want ERROR_ALREADY_EXISTS", err)
	}
}

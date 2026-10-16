//go:build windows && service
// +build windows,service

package main

import (
	"fmt"
	"os/exec"
)

// runUpdateTask registers a one-time task that runs cmdPath as SYSTEM and
// starts it now. A task rather than a child process: msiexec stops this
// service, and a child of it would go with it. /tr names the batch file, not
// the msiexec line, because /tr is limited to 261 characters. The start time
// is only required by /SC ONCE; /Run starts it (schtasks warns that 00:00 has
// passed and creates it anyway). No /SD: its format follows the locale. The
// path is quoted inside /TR in case ProgramData has a space in it.
func runUpdateTask(name, cmdPath string) error {
	create := exec.Command("schtasks.exe", "/Create", "/F", "/TN", name,
		"/TR", `"`+cmdPath+`"`, "/SC", "ONCE", "/ST", "00:00",
		"/RU", "SYSTEM", "/RL", "HIGHEST")
	if out, err := create.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Create: %v: %s", err, string(out))
	}
	run := exec.Command("schtasks.exe", "/Run", "/TN", name)
	if out, err := run.CombinedOutput(); err != nil {
		deleteUpdateTask(name)
		return fmt.Errorf("schtasks /Run: %v: %s", err, string(out))
	}
	return nil
}

func deleteUpdateTask(name string) {
	_ = exec.Command("schtasks.exe", "/Delete", "/F", "/TN", name).Run()
}

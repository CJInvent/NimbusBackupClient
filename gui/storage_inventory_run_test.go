package main

// NimbusControl docs/V4-FIRST-ENROLLMENT.md section 3.4 (and 10.1): a failed
// storage inventory says why, and a transient failure is retried, boundedly,
// before it latches an error. The retry and the wording are tested here on
// any OS; the PowerShell query itself only runs on Windows.

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeInventoryClock advances only when the code under test sleeps, or when
// an attempt says it took time.
type fakeInventoryClock struct {
	t      time.Time
	sleeps []time.Duration
}

func (f *fakeInventoryClock) clock() inventoryClock {
	return inventoryClock{
		now:   func() time.Time { return f.t },
		sleep: func(d time.Duration) { f.sleeps = append(f.sleeps, d); f.t = f.t.Add(d) },
	}
}

func TestStorageInventoryRetrySucceedsWithoutAnError(t *testing.T) {
	fc := &fakeInventoryClock{t: time.Unix(1_700_000_000, 0)}
	calls := 0
	out, err := retryStorageInventory(func() ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("exit status 1: Get-Disk : The device is busy")
		}
		return []byte("[]"), nil
	}, fc.clock())
	if err != nil || string(out) != "[]" || calls != 2 {
		t.Fatalf("a transient failure was not retried away: out=%q err=%v calls=%d", out, err, calls)
	}
	if len(fc.sleeps) != 1 || fc.sleeps[0] != 2*time.Second {
		t.Fatalf("sleeps %v, want one 2s pause", fc.sleeps)
	}
}

func TestStorageInventoryFirstSuccessDoesNotWait(t *testing.T) {
	fc := &fakeInventoryClock{t: time.Unix(1_700_000_000, 0)}
	if _, err := retryStorageInventory(func() ([]byte, error) { return []byte("[]"), nil }, fc.clock()); err != nil {
		t.Fatal(err)
	}
	if len(fc.sleeps) != 0 {
		t.Fatalf("slept %v on a first success", fc.sleeps)
	}
}

func TestStorageInventoryRetryIsBoundedByAttemptsAndSaysSo(t *testing.T) {
	fc := &fakeInventoryClock{t: time.Unix(1_700_000_000, 0)}
	calls := 0
	_, err := retryStorageInventory(func() ([]byte, error) {
		calls++
		return nil, errors.New("exit status 1: Get-Disk : Access is denied.")
	}, fc.clock())
	if calls != storageInventoryAttempts {
		t.Fatalf("%d attempts, want %d", calls, storageInventoryAttempts)
	}
	if err == nil || err.Error() != "exit status 1: Get-Disk : Access is denied. (3 attempts)" {
		t.Fatalf("error %v: want the last reason and the count", err)
	}
	if len(fc.sleeps) != 2 || fc.sleeps[0] != 2*time.Second || fc.sleeps[1] != 5*time.Second {
		t.Fatalf("sleeps %v, want 2s then 5s", fc.sleeps)
	}
}

// A 30-second timeout repeated costs one retry, not two: no attempt starts
// once 40 seconds have passed.
func TestStorageInventoryRetryIsBoundedByTime(t *testing.T) {
	fc := &fakeInventoryClock{t: time.Unix(1_700_000_000, 0)}
	calls := 0
	_, err := retryStorageInventory(func() ([]byte, error) {
		calls++
		fc.t = fc.t.Add(storageInventoryTimeout)
		return nil, errors.New("timed out after 30s")
	}, fc.clock())
	if calls != 2 {
		t.Fatalf("%d attempts of 30s each, want 2 (the time bound)", calls)
	}
	if err == nil || !strings.HasSuffix(err.Error(), "(2 attempts)") {
		t.Fatalf("error %v: want the count", err)
	}
}

func TestStorageInventoryOneAttemptNamesNoCount(t *testing.T) {
	saved := storageInventoryAttempts
	defer func() { storageInventoryAttempts = saved }()
	storageInventoryAttempts = 1
	fc := &fakeInventoryClock{t: time.Unix(1_700_000_000, 0)}
	_, err := retryStorageInventory(func() ([]byte, error) { return nil, errors.New("exit status 1") }, fc.clock())
	if err == nil || err.Error() != "exit status 1" {
		t.Fatalf("error %v: a single attempt needs no count", err)
	}
}

// What PowerShell prints for a terminating error, as it reaches a parent
// process: the message, the position, the echoed line and its underline, and
// the category. Only the position, the echo and the underline are noise.
func TestCondenseInventoryStderrKeepsTheReason(t *testing.T) {
	stderr := "Get-Disk : Access is denied.\r\n" +
		"At line:2 char:11\r\n" +
		"+ $items=@(Get-Disk | ForEach-Object {\r\n" +
		"+          ~~~~~~~~\r\n" +
		"    + CategoryInfo          : PermissionDenied: (MSFT_Disk:ROOT/Microsoft/...k) [Get-Disk], CimException\r\n" +
		"    + FullyQualifiedErrorId : HRESULT 0x80070005,Get-Disk\r\n" +
		" \r\n"
	got := condenseInventoryStderr([]byte(stderr))
	want := "Get-Disk : Access is denied. | CategoryInfo : PermissionDenied: (MSFT_Disk:ROOT/Microsoft/...k) [Get-Disk], CimException | FullyQualifiedErrorId : HRESULT 0x80070005,Get-Disk"
	if got != want {
		t.Fatalf("condensed:\n got %q\nwant %q", got, want)
	}
}

// PowerShell writes its error stream as CLIXML when it thinks its parent is
// another PowerShell; the reason is inside the Error strings.
func TestCondenseInventoryStderrReadsCLIXML(t *testing.T) {
	stderr := `#< CLIXML` + "\r\n" +
		`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04"><S S="Error">Get-Disk : Invalid class &amp; namespace_x000D__x000A_</S><S S="Error">At line:2 char:11_x000D__x000A_</S><S S="Error">    + FullyQualifiedErrorId : HRESULT 0x8004100e,Get-Disk_x000D__x000A_</S></Objs>`
	got := condenseInventoryStderr([]byte(stderr))
	want := "Get-Disk : Invalid class & namespace | FullyQualifiedErrorId : HRESULT 0x8004100e,Get-Disk"
	if got != want {
		t.Fatalf("condensed:\n got %q\nwant %q", got, want)
	}
}

func TestCondenseInventoryStderrIsBounded(t *testing.T) {
	long := strings.Repeat("é", 2000) // 2 bytes each: the cut must not split one
	got := condenseInventoryStderr([]byte(long))
	if len(got) > storageInventoryStderrMax || !utf8.ValidString(got) || got == "" {
		t.Fatalf("len %d valid %v", len(got), utf8.ValidString(got))
	}
}

// The real exec path, with a real exit status and a real stderr: what the
// machine page gets when the query fails.
func TestRunInventoryCommandNamesTheExitStatusAndStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	out, err := runInventoryCommand(10*time.Second, nil, "sh", "-c", "echo partial; echo 'Get-Disk : Access is denied.' >&2; exit 3")
	if err == nil {
		t.Fatalf("a failed command returned %q and no error", out)
	}
	if err.Error() != "exit status 3: Get-Disk : Access is denied." {
		t.Fatalf("error %q", err)
	}
	out, err = runInventoryCommand(10*time.Second, nil, "sh", "-c", "echo '[1]'")
	if err != nil || strings.TrimSpace(string(out)) != "[1]" {
		t.Fatalf("success: %q %v", out, err)
	}
	_, err = runInventoryCommand(10*time.Second, nil, "sh", "-c", "exit 1")
	if err == nil || err.Error() != "exit status 1" {
		t.Fatalf("no stderr: %v", err)
	}
}

func TestRunInventoryCommandSaysItTimedOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	_, err := runInventoryCommand(200*time.Millisecond, nil, "sh", "-c", "sleep 5")
	if err == nil || err.Error() != "timed out after 200ms" {
		t.Fatalf("error %v: want the timeout named, not a signal", err)
	}
}

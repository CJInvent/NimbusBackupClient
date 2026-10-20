//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// webView2ClientKey is where the Evergreen runtime records its version (`pv`)
// when installed per-machine: Microsoft's documented detection method
// ("Detect if a WebView2 Runtime is already installed"). The WOW6432Node path
// is 64-bit Windows; the other is 32-bit Windows.
var webView2ClientKeys = []string{
	`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
	`SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
}

// webView2BootstrapperURL is Microsoft's Evergreen Bootstrapper, the link the
// WebView2 download page gives and the one Wails' downloader used.
const webView2BootstrapperURL = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

// webView2MaxBytes bounds the download. The bootstrapper is under 2 MB; it
// fetches the runtime itself.
const webView2MaxBytes = 16 << 20

// webView2Signer is the publisher the bootstrapper must be signed by before
// the service runs it as SYSTEM.
const webView2Signer = "Microsoft Corporation"

// installedWebView2Version is the per-machine runtime's version, or "".
func installedWebView2Version() string {
	for _, path := range webView2ClientKeys {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			continue
		}
		pv, _, err := k.GetStringValue("pv")
		_ = k.Close()
		if err == nil && webView2Usable(pv) {
			return strings.TrimSpace(pv)
		}
	}
	return ""
}

// ensureWebView2Runtime installs the Evergreen runtime machine-wide when no
// usable one is installed. Called once, in the background, at service start;
// a failure is logged and tried again at the next start.
func ensureWebView2Runtime() {
	if v := installedWebView2Version(); v != "" {
		writeInfoLog(fmt.Sprintf("[WebView2] runtime %s is installed", v))
		return
	}
	writeInfoLog("[WebView2] no usable runtime (the console needs " + webView2MinVersion + " or later): installing it")
	if err := installWebView2(); err != nil {
		writeWarnLog(fmt.Sprintf("[WebView2] the runtime was not installed, and the console cannot open until it is: %v. The service tries again when it next starts.", err))
		return
	}
	if v := installedWebView2Version(); v != "" {
		writeInfoLog(fmt.Sprintf("[WebView2] runtime %s installed", v))
		return
	}
	writeWarnLog("[WebView2] the installer finished, but no runtime is registered; the console cannot open until one is")
}

// installWebView2 downloads the bootstrapper into a folder only SYSTEM and
// Administrators can write, checks its signature, and runs it silently. Run
// as SYSTEM, the bootstrapper installs per-machine.
func installWebView2() error {
	base, err := getConfigDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(base, "webview2")
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("could not clear %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// ProgramData lets users create files; an executable SYSTEM is about to
	// run must not sit where one could be swapped in.
	if err := restrictToServiceOnly(dir); err != nil {
		return fmt.Errorf("could not restrict %s: %w", dir, err)
	}

	setup := filepath.Join(dir, "MicrosoftEdgeWebview2Setup.exe")
	if err := downloadWebView2Bootstrapper(setup); err != nil {
		return err
	}
	if err := verifySignedBy(setup, webView2Signer); err != nil {
		return fmt.Errorf("the downloaded installer was not run: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	// #nosec G204 -- our own path, in a folder only SYSTEM and Administrators can write, signature checked above
	out, err := exec.CommandContext(ctx, setup, "/silent", "/install").CombinedOutput()
	if err != nil {
		return fmt.Errorf("the installer failed: %w %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// downloadWebView2Bootstrapper fetches the bootstrapper over HTTPS, following
// redirects only to Microsoft's own hosts.
func downloadWebView2Bootstrapper(path string) error {
	client := &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many redirects")
			}
			if !microsoftHTTPS(req.URL.Scheme, req.URL.Hostname()) {
				return fmt.Errorf("refusing a redirect to %s://%s", req.URL.Scheme, req.URL.Host)
			}
			return nil
		},
	}
	resp, err := client.Get(webView2BootstrapperURL)
	if err != nil {
		return fmt.Errorf("could not download the installer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("could not download the installer: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- path chosen by installWebView2
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, webView2MaxBytes+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("could not download the installer: %w", copyErr)
	case closeErr != nil:
		return closeErr
	case n > webView2MaxBytes:
		return fmt.Errorf("the installer is larger than %d bytes; refusing it", webView2MaxBytes)
	}
	return nil
}

// modcrypt32 is secrets_windows.go's.
var (
	procCryptMsgGetParam = modcrypt32.NewProc("CryptMsgGetParam")
	procCryptMsgClose    = modcrypt32.NewProc("CryptMsgClose")
)

// cmsgSignerInfoParam is CMSG_SIGNER_INFO_PARAM.
const cmsgSignerInfoParam = 6

// cmsgSignerInfo is the head of CMSG_SIGNER_INFO: the two fields that name
// the signer's certificate. The rest of the structure is not read.
type cmsgSignerInfo struct {
	Version      uint32
	Issuer       windows.CertNameBlob
	SerialNumber windows.CryptIntegerBlob
}

// verifySignedBy requires a valid Authenticode signature on path, chaining to
// a trusted root, whose signing certificate's name is signer.
func verifySignedBy(path, signer string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	data := &windows.WinTrustData{
		Size:             uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:         windows.WTD_UI_NONE,
		RevocationChecks: windows.WTD_REVOKE_WHOLECHAIN,
		UnionChoice:      windows.WTD_CHOICE_FILE,
		StateAction:      windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&windows.WinTrustFileInfo{
			Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
			FilePath: p,
		}),
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	if verifyErr != nil {
		return fmt.Errorf("%s has no valid signature: %w", filepath.Base(path), verifyErr)
	}

	name, err := signerName(p)
	if err != nil {
		return fmt.Errorf("%s's signer could not be read: %w", filepath.Base(path), err)
	}
	if name != signer {
		return fmt.Errorf("%s is signed by %q, not %q", filepath.Base(path), name, signer)
	}
	return nil
}

// signerName is the simple display name of the certificate that signed a
// file's embedded signature.
func signerName(path *uint16) (string, error) {
	var enc, contentType, formatType uint32
	var store, msg windows.Handle
	if err := windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(path),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED, windows.CERT_QUERY_FORMAT_FLAG_BINARY, 0,
		&enc, &contentType, &formatType, &store, &msg, nil); err != nil {
		return "", err
	}
	defer func() { _ = windows.CertCloseStore(store, 0) }()
	defer func() { _, _, _ = procCryptMsgClose.Call(uintptr(msg)) }()

	var size uint32
	if r, _, e := procCryptMsgGetParam.Call(uintptr(msg), cmsgSignerInfoParam, 0, 0, uintptr(unsafe.Pointer(&size))); r == 0 {
		return "", fmt.Errorf("signer info size: %w", e)
	}
	if size < uint32(unsafe.Sizeof(cmsgSignerInfo{})) {
		return "", fmt.Errorf("signer info is %d bytes", size)
	}
	// uint64s, so the buffer is aligned for the pointers inside it.
	buf := make([]uint64, (size+7)/8)
	if r, _, e := procCryptMsgGetParam.Call(uintptr(msg), cmsgSignerInfoParam, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return "", fmt.Errorf("signer info: %w", e)
	}
	si := (*cmsgSignerInfo)(unsafe.Pointer(&buf[0]))

	find := windows.CertInfo{Issuer: si.Issuer, SerialNumber: si.SerialNumber}
	cert, err := windows.CertFindCertificateInStore(store, enc, 0, windows.CERT_FIND_SUBJECT_CERT, unsafe.Pointer(&find), nil)
	if err != nil {
		return "", fmt.Errorf("the signing certificate: %w", err)
	}
	defer func() { _ = windows.CertFreeCertificateContext(cert) }()

	n := windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, nil, 0)
	if n <= 1 {
		return "", errors.New("the signing certificate has no name")
	}
	name := make([]uint16, n)
	windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, &name[0], n)
	return windows.UTF16ToString(name), nil
}

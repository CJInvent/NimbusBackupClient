package controlplane

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// AlternateWANURL is the internet echo service asked for the Alternate WAN
// IP: the address the internet sees this machine as. Contacted only while the
// organization's report_alternate_wan policy is on (docs/AGENT-API.md in
// NimbusControl), so a machine in an org that did not ask for it never makes
// this request.
const AlternateWANURL = "https://ipecho.net/plain"

// alternateWANTimeout bounds one lookup. The check-in must not wait on a
// third party: on any failure the field is simply omitted for that cycle.
const alternateWANTimeout = 5 * time.Second

// LookupAlternateWAN asks the echo service at url what address it sees this
// machine at. It returns a routable address or an error; it never retries.
//
// The body is capped and parsed as an address rather than trusted as text, and
// a private, loopback or link-local answer is rejected: an echo service cannot
// see a machine at such an address, so one means a captive portal, a proxy
// error page or a hostile reply, and reporting it would be reporting nothing.
func LookupAlternateWAN(ctx context.Context, client *http.Client, url string) (string, error) {
	if client == nil {
		client = &http.Client{}
	}
	ctx, cancel := context.WithTimeout(ctx, alternateWANTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("echo service answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return "", fmt.Errorf("echo service did not return an address")
	}
	addr = addr.Unmap()
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || cgnat.Contains(addr) {
		return "", fmt.Errorf("echo service returned a non-routable address")
	}
	return addr.String(), nil
}

//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// The MSI's PROVISIONING_PROFILE property lands here (installer/wix/Product.wxs;
// NimbusControl docs/V4-CLIENT-RELEASES.md 4.1): base64 of the profile JSON,
// in its own subkey whose ACL is SYSTEM and Administrators only, so the
// enrollment token in it is not readable by users the way the rest of
// HKLM\SOFTWARE\NimbusBackup is.
const (
	provisioningRegKey   = `SOFTWARE\NimbusBackup\Provisioning`
	provisioningRegValue = "Profile"
)

func readProvisioningRegistry() (string, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, provisioningRegKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", false
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetStringValue(provisioningRegValue)
	if err != nil || v == "" {
		return "", false
	}
	return v, true
}

// deleteProvisioningRegistry overwrites the value, then removes the subkey.
func deleteProvisioningRegistry() error {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, provisioningRegKey, registry.SET_VALUE|registry.WOW64_64KEY); err == nil {
		_ = k.SetStringValue(provisioningRegValue, "")
		_ = k.Close()
	}
	err := registry.DeleteKey(registry.LOCAL_MACHINE, provisioningRegKey)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}

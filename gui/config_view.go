package main

// configView is the configuration as the console may see it: what GET /status
// returns under "configuration" (docs/V4-CONSOLE.md §3, GetConfigWithHostname
// and GetLastBackupDirs).
//
// One builder for both builds until V4-CONSOLE C2 removes the console's own
// copy of config.json. Before it, the service answered /status with five of
// these fields and the console read the rest from its own decrypted copy, so
// moving the console onto /status would have emptied its settings form.
//
// NO SECRET, ONLY WHETHER ONE IS STORED (M-04). "secret" is always empty: the
// form submits an empty secret to mean "keep the stored one", and the service
// merges it back (SaveConfigFromMap).
func configView(cfg *Config, hostname string) map[string]interface{} {
	if cfg == nil {
		cfg = &Config{}
	}
	backupID := cfg.BackupID
	if backupID == "" {
		backupID = hostname
	}
	lastDirs := cfg.LastBackupDirs
	if lastDirs == nil {
		lastDirs = []string{}
	}
	return map[string]interface{}{
		"baseurl":                 cfg.BaseURL,
		"certfingerprint":         cfg.CertFingerprint,
		"authid":                  cfg.AuthID,
		"secret":                  "",
		"secret_set":              cfg.Secret != "",
		"datastore":               cfg.Datastore,
		"namespace":               cfg.Namespace,
		"backupdir":               cfg.BackupDir,
		"backup-id":               backupID,
		"usevss":                  cfg.UseVSS,
		"upload_limit_mbps":       cfg.UploadLimitMbps,
		"control_server_url":      cfg.ControlServerURL,
		"control_enrolled":        cfg.ControlAgentID > 0,
		"exchange_aware":          cfg.ExchangeAware,
		"exchange_log_truncation": cfg.ExchangeLogTruncation,
		"last_backup_dirs":        lastDirs,
		"hostname":                hostname,
	}
}

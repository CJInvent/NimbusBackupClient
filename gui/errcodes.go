package main

// Canonical error codes for user-facing errors.
//
// Contract: a user-facing error string is "[NB-xxxx] <english base>" and may
// carry dynamic detail after a " :: " separator. Logs always record the code
// plus the English base (grep-able, language-stable for support); the GUI
// recognizes the [NB-xxxx] token and swaps the base for the active language's
// translation (translations.js err_NBxxxx keys, FR/EN/ES), keeping any detail.
//
// Codes: 1xxx = configuration/validation, 2xxx = backup execution.
const (
	errServerURLRequired = "[NB-1001] PBS server URL required"
	errAuthIDRequired    = "[NB-1002] authentication ID required"
	errSecretRequired    = "[NB-1003] secret required"
	errDatastoreRequired = "[NB-1004] datastore required"
	errInvalidURL        = "[NB-1005] invalid URL"
	errNoPBSServer       = "[NB-1006] no PBS server specified and no default server"
	errFingerprintSave   = "[NB-1010] cannot save fingerprint (config.json not writable?)"
	// NB-1011 (certificate not trusted, no fingerprint pinned) is the
	// console's: it reads the service's cert_untrusted flag (console/errcodes.go).

	errBackupFailedSeeLog = "[NB-2001] Backup failed - see backup log in C:\\ProgramData\\NimbusBackup"
	errDiskRequired       = "[NB-2002] at least one physical disk required"
	errAdminRequired      = "[NB-2003] full-disk backup requires administrator privileges - relaunch as administrator or install the Nimbus service"
	errAlreadyRunning     = "[NB-2004] a backup to this destination is already running - not starting another"
	errPBSParamsRequired  = "[NB-2005] PBS connection parameters required"
	errVSSAdminRequired   = "[NB-2006] VSS (Shadow Copy) requires administrator privileges - relaunch as administrator or disable VSS"
	errDirRequired        = "[NB-2008] at least one backup directory required"
	errBackupStopped      = "[NB-2009] Backup stopped - the partial backup was discarded and the snapshot released"
	errNoBackupRunning    = "[NB-2010] no backup is running"
	// NB-2011 (the service is not reachable) is the console's own
	// (console/errcodes.go): only a client can fail to reach the service.
	// A job named a drive this machine cannot resolve to a disk. Distinct
	// from NB-2002, which means the job named no disk at all: this one means
	// the operator asked for something specific and it is not here.
	errStorageIdentity = "[NB-2013] storage identity requires manual intervention"
	errDiskUnresolved  = "[NB-2012] backup target is not a disk on this machine"
	errServerIDExists  = "[NB-1007] a PBS server with this ID already exists"

	errSnapshotList       = "[NB-3001] listing snapshots failed"
	errFolderPickerSvc    = "[NB-3002] folder picker unavailable in service mode - enter the destination path manually"
	errInPlaceNoMeta      = "[NB-3003] in-place restore impossible: this snapshot has no metadata (.nimbus_backup_meta.json missing) - choose an alternate location"
	errInPlaceNoOrigPath  = "[NB-3004] in-place restore impossible: the original path is not recorded in the metadata"
	errInPlaceCrossHost   = "[NB-3005] in-place restore blocked: backup is from another machine - tick 'force cross-host' if this is deliberate"
	errInvalidRegex       = "[NB-3006] invalid regular expression"
	errBackupIDRequired   = "[NB-3007] backup ID required"
	errSnapshotIDRequired = "[NB-3008] snapshot ID required"
	errDestPathRequired   = "[NB-3009] destination folder required"
	errInPlaceOSMismatch  = "[NB-3010] in-place restore impossible: backup was made on a different OS"
	errReadHostname       = "[NB-3011] cannot read local hostname"
	errSearchTermRequired = "[NB-3012] search term required"
	// A restore destination security.ValidatePath refuses (a ".." or a NUL).
	// Checked by the service, the process that writes there as SYSTEM; the
	// console checked it alone until V4-CONSOLE C3, where any other caller of
	// the local API skipped it.
	errDestPathInvalid    = "[NB-3013] destination path refused"
	errServerNameRequired = "[NB-1008] PBS server name required"
)

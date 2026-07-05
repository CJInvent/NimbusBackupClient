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

	errBackupFailedSeeLog = "[NB-2001] Backup failed - see backup log in C:\\ProgramData\\NimbusBackup"
	errDiskRequired       = "[NB-2002] at least one physical disk required"
	errAdminRequired      = "[NB-2003] full-disk backup requires administrator privileges - relaunch as administrator or install the Nimbus service"
	errAlreadyRunning     = "[NB-2004] a backup to this destination is already running - not starting another"
	errPBSParamsRequired  = "[NB-2005] PBS connection parameters required"
	errVSSAdminRequired   = "[NB-2006] VSS (Shadow Copy) requires administrator privileges - relaunch as administrator or disable VSS"
	errServiceComm        = "[NB-2007] communication with the service failed"
	errDirRequired        = "[NB-2008] at least one backup directory required"
	errServerIDExists     = "[NB-1007] a PBS server with this ID already exists"

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
	errServerNameRequired = "[NB-1008] PBS server name required"
)

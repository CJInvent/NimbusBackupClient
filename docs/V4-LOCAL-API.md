# v4: the local API knows who is calling

**Status (2026-10-20).** P1 built (section 10, "P1 as built"); P2 to P5 not
started. Specified 2026-10-17. Companion to
`docs/V4-CONSOLE.md`, whose finding F1 this answers and whose decision D1 and
phase C4 it settles. Decisions are CJ's (2026-10-17) unless marked as a
recommendation.

---

## 1. The problem

The console talks to the service over HTTP on `127.0.0.1:18765`, authenticated
by a shared token in `ProgramData\NimbusBackup\api-token`. The token file's ACL
grants SYSTEM, Administrators and **INTERACTIVE**, which is every interactively
logged-on user, Remote Desktop sessions included (`api/acl_windows.go`). The
service checks the token, never the caller. Every logged-on user therefore has
the same power over a service running as SYSTEM.

The sharpest case is restore. A restore request carries its destination
(`DestPath`, or `RestoreModeOriginal` for the original paths); the service
writes there as SYSTEM and, for volume-file restores, applies the stored DACLs
(`restore_acl_windows.go`). No destination check was found in the restore
path. On a terminal server, a user who can get a file into a backup can have the
service place it in `C:\Windows\System32`. Browsing has the same shape on the
read side: any logged-on user can list every file of every profile in a backup
of the machine. Org `file_restore` policy gates both on managed machines; a
standalone install permits both. Read from the code, not reproduced on a
machine.

`V4-CONSOLE.md` makes this API the console's only path to anything, which makes
this boundary the one that matters.

## 2. Decisions (CJ, 2026-10-17)

1. **A named pipe replaces the loopback port**, provided it reduces size,
   improves security and allows UAC elevation for browse and restore. It does
   all three (sections 3, 6, 9).
2. **The service decides from the caller's Windows identity**, never from
   anything the caller says about itself.
3. **Elevation spans a browse session (option B):** the Browse tab runs in an
   elevated console window for as long as it is open; leaving the tab ends it.
   The alternative (the service remembering an approval for an unelevated
   window) was rejected: malware running as the user can inject into an
   unelevated process and inherit the approval, while Windows' integrity
   levels stop it from tampering with an elevated one.
4. **No UAC prompt on entering the tab.** The Browse tab has an
   **Elevate permissions** button near the top; the prompt appears only when it
   is pressed (section 6).
5. **The folder picker moves into the service** (V4-CONSOLE D1), listing as the
   caller (section 5.3). D1's reason for keeping it in the console was this
   finding; with caller identity, the service is the right home.

## 3. The pipe

**Name and creation.** `\\.\pipe\NimbusBackup`, created by the service at
start with `FILE_FLAG_FIRST_PIPE_INSTANCE` on the first instance, so a process
that created the name first makes the service fail loudly at start instead of
being impersonated. `PIPE_REJECT_REMOTE_CLIENTS` on every instance.

**DACL.** SYSTEM and Administrators full control; INTERACTIVE read/write (no
`FILE_CREATE_PIPE_INSTANCE`, so nobody else can add server instances);
NETWORK denied. Built with `x/sys/windows` security descriptor calls, which the
service already depends on. No new module.

**Framing.** HTTP/1.1 over the pipe, **one request per connection**. The
existing mux, handlers, `readonly.go` allowlist and `api/restore.go` gate stay
as they are; only the listener and the client transport change. One request per
connection means a connection's identity can never be confused with another's.
The server wraps the pipe in a `net.Listener` and keeps using `http.Serve`.
The client is `CreateFile` on the pipe path, `req.Write`, `http.ReadResponse`:
no `http.Transport`, no TLS, no proxy or HTTP/2 code. That is the transport
V4-CONSOLE C4 called for (prototype measured at 9,318,400 bytes with Wails'
WebView2 downloader also removed).

**Caller identity, per connection.** Before reading the request, the service
calls `ImpersonateNamedPipeClient`, opens the thread token, reads what it needs,
and calls `RevertToSelf`; then `GetNamedPipeClientProcessId`. It attaches to
the request context, and nothing else in the service may establish identity:

| Field | From |
|---|---|
| `user_sid` | `TokenUser` |
| `session_id` | `TokenSessionId` |
| `elevated` | `TokenElevation` |
| `admin` | Administrators SID present **and enabled** (`CheckTokenMembership`), which is true only in an elevated or non-UAC admin token |
| `pid`, `image` | client process id and image path, for the log only, never for a decision |
| `token` | a duplicated impersonation token, kept for the request's lifetime so section 5 can act as the caller, closed when the request ends |

**The client checks the server.** The console opens the pipe with
`SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION`, so a server can identify the
console but not act as it. Before sending anything it calls
`GetNamedPipeServerProcessId` and refuses unless that process runs as SYSTEM
from `%ProgramFiles%\NimbusBackup\NimbusBackupSVC.exe`.

**Removed:** the token file and everything about it (`EnsureToken`,
`restrictToOwners`, `tokenTransport`, the `X-Nimbus-Token` header), port 18765,
and the Origin and custom-header CSRF defenses, which existed only because a
browser can reach a loopback port. A browser cannot open a pipe.

## 4. Who may do what

Every route gets an entry in one table in `gui/api`, in the style of
`readonly.go` and the restore op table: an undeclared route is refused, and the
declaration is where the requirement is written down. Requirements:

- **user**: any interactive caller.
- **admin**: `admin` true (elevated).
- **user, scoped**: any caller, with the effect limited to what that account
  can already reach (section 5).

| Area | Routes | Requirement |
|---|---|---|
| Status | `/status`, `/connections`, `/runs/*`, `/controlplane/status`, `/storage/identity` (GET) | user |
| Backups | `POST /backup`, `/backup/cancel` | user (as today; org policy and the unmanaged-backup gate still apply) |
| Local jobs | `/jobs` (GET) user; create, update, delete | admin |
| Settings | `/pbs/*`, `/config/save`, `/controlplane/save`, `/storage/identity` (POST), `/pbs/test`, `/pbs/discover` | admin |
| Machine facts | `/disks`, `/exchange` | user |
| Browse and restore | `/restore/*` | user, scoped; admin unscoped (section 5) |
| Folder picker | `/fs/list`, `/fs/mkdir` (new, section 5.3) | user, scoped |

**Behavior change:** settings and job writes need elevation. Today any
token holder can repoint the machine at another PBS server or control server.
The Setup screen is standalone-only after GUI audit S6, and a standalone
machine's owner is normally its administrator; the Setup and job screens get
the same Elevate button as Browse when they are not elevated (section 6.4).

## 5. Browse and restore as the caller

### 5.1 Browsing

An **admin** caller sees the whole backup, as today.

A **user** caller sees only entries their account could read. Both backup kinds
carry the source's permissions: directory backups store each entry's security
descriptor in the `nimbus-acls.json.gz.blob` side-car (`backup_meta.go`), and
volume images carry NTFS descriptors (`imagebrowse`). For every entry the
service runs `AccessCheck(storedSD, callerToken, FILE_READ_DATA)` (directories:
`FILE_LIST_DIRECTORY`) and omits what fails. An entry with no stored
descriptor is omitted for a user caller. Search applies the same filter to
every hit.

What this does and does not mean: it answers "could this account have read
this file when it was backed up", using the SIDs and groups in the caller's
token today. On a machine where the backup came from another machine, the
stored SIDs will not match local accounts and a user caller sees nothing,
which is correct.

### 5.2 Restoring and downloading

- **user**: the destination must resolve (after following junctions and
  symbolic links, `GetFinalPathNameByHandle` on the opened directory) inside
  the caller's own profile directory. Every write happens while impersonating
  the caller, so NTFS enforces the caller's own rights and files are owned by
  them. Stored DACLs are not applied for a user caller (the files get the
  destination's inherited permissions). In-place restore (`RestoreModeOriginal`)
  is refused. Only entries that passed 5.1's filter can be requested; the
  service re-checks rather than trusting the selection.
- **admin**: any destination, in-place restore, stored DACLs applied, as today.

Downloads (zip and volume-file extraction) follow the same rules, since they
are writes to a destination.

### 5.3 The folder picker

`ListDrives`, `ListFolders`, `CreateFolder` and `DefaultSaveDir` become
`/fs/list` and `/fs/mkdir`, served while impersonating the caller, so the
listing is exactly what that account can see and a created folder is created
with its rights. `DefaultSaveDir` is the caller's profile Downloads folder,
resolved from the caller's token (`SHGetKnownFolderPath` with the token). The
confinement rules of `controlplane_fsbrowse.go` (absolute local paths, no
network or device paths) apply to both callers of the lister, the portal and
the console. The 178 lines of `pathpicker.go` leave the console.

## 6. Elevation

### 6.1 The button

The Browse tab never prompts on entry. Near the top of the tab, beside the
backup list's heading, is **Elevate permissions**, with the Windows shield
symbol.

It is shown when the console window is **not elevated**, and hidden when it
is. Being a member of Administrators is not enough to hide it. By default
Windows runs administrators unelevated until they approve a UAC prompt, so a
local admin's normal window cannot restore outside their profile either. Hiding
the button for them would leave no way to elevate. Concretely:

| Who | Window | Button |
|---|---|---|
| Standard user | normal | shown; UAC asks for an administrator's credentials |
| Administrator | normal (UAC filtered) | shown; UAC asks for consent |
| Administrator | elevated, or UAC disabled | hidden |

Beside the button, one line says what changes: "Browsing shows the files your
account can read, and restores go to your own folders. Elevate to see every
file and restore anywhere." Hidden together with the button.

### 6.2 What pressing it does

The console starts a second instance of itself with `ShellExecuteEx`, verb
`runas`, arguments `--elevated-browse`, plus the theme, accent, font size,
language and the snapshot currently open, so the elevated window looks the same
and opens where the user was. Then:

- **UAC cancelled**: nothing changes; a notice says the permissions were not
  changed. No retry, no second prompt.
- **UAC approved**: the normal window hides (it keeps the tray icon) and the
  elevated window opens on the Browse tab, with an "Administrator" badge in the
  header.

### 6.3 The elevated window

- Shows the Browse tab only. Selecting any other tab, or closing the window,
  ends the session: the elevated process exits with the tab chosen, and the
  normal window, which waited on the process handle `ShellExecuteEx` returned,
  reappears on that tab.
- Has no tray icon, no scheduler or settings views, and its own single-instance
  lock and WebView2 data folder (`WebView2-admin`), so it never shares state
  with the unelevated window's web profile.
- Carries no grant or session state in the service. Every request it makes is
  judged by its token (section 3); when it exits, nothing remains to expire or
  revoke.
- If the user signs in to UAC with **another** administrator's credentials, the
  elevated window runs as that account. "Your own folders" then means that
  administrator's profile, and the log records both accounts (the window's
  token and the session's interactive user).

### 6.4 The same pattern elsewhere

Setup (standalone) and the local-jobs editor need admin under section 4. They
get the same button and the same elevated-window pattern, each showing only its
own tab. One mechanism, three entry points.

## 7. Can the console or the service be fooled?

- **A modified or replacement console** gets the rights of the account it runs
  as, because the service reads identity from Windows and ignores the request's
  claims. The console holds nothing worth stealing after V4-CONSOLE C2.
- **Malware running as the user** can use the pipe with that user's rights,
  which it already has. It cannot reach an elevated window (integrity levels),
  and there is no stored approval to ride.
- **A fake service** (a process that creates the pipe name first) is stopped
  twice: the real service refuses to start without the first instance, and the
  console verifies the server process before sending a byte.
- **The network**: refused by the pipe flag and the DACL.
- **What no design stops**: a local administrator can replace or patch the
  service itself. Program Files is writable only by administrators, and
  Authenticode signing (`docs/CODE-SIGNING.md`, pending) is what makes a
  modified binary visible.

## 8. Logging and audit

Every mutating request logs the caller's account, session, elevated flag and
client image path in the service log. Restores and downloads already report to
the control plane as first-class events (V4-STATUS, "Restores are reported");
the event gains `caller_account` and `elevated`, so the portal can tell an
administrator's restore to `System32` from a user's restore to their Documents.

## 9. Size

The console side of the pipe is a file open and two `net/http` helper calls.
With Wails' WebView2 downloader moved to the service (V4-CONSOLE D2), the
console measured 9,318,400 bytes stripped against 13,268,992 today. The service
gains the pipe listener, identity, access checks and impersonated writes
(estimated under 100 KB, using `x/sys/windows` and the standard library only)
and loses the token code.

## 10. The work, in order

These follow V4-CONSOLE's C1 to C3 (the console must already be API-only and
its own module) and replace C4's loopback client.

| # | Pull request | Tests first |
|---|---|---|
| P1 | **The pipe and the identity.** Listener, DACL, first-instance flag, per-connection identity on the request context, console client with server verification. Token file, port, Origin checks deleted. V4-CONSOLE D2's runtime check moves into the service; console built with `wv2runtime.error` | Windows CI: a client in the normal runner session gets `elevated=false`; a second pipe-name creator before the service makes the service fail to start; a remote-style connection is refused; a server not running as SYSTEM is refused by the client; a request that claims an identity in its body gets the token's |
| P2 | **The route table** (section 4). Settings and job writes require admin | Every route declared (an undeclared one refused); each admin route refused with `elevated=false` and served with `true`; `readonly.go` still applies on top |
| P3 | **Browse filtering and scoped restore** (section 5), the picker in the service | Fixture snapshots with stored descriptors for two SIDs: a user sees only theirs, search included; a destination outside the profile, a junction pointing out of it, and in-place mode are each refused for a user; a user restore runs impersonated (owner of the written file is the caller); admin unchanged |
| P4 | **The Elevate button and the elevated window** (section 6), in Browse, Setup and Jobs | Render tests: button shown for `elevated=false` (both rows of 6.1), hidden for `true`; no UAC call on tab entry (the binding is only called from the button handler); Windows CI: an elevated child started with `--elevated-browse` reports `elevated=true` to the service and exits with the chosen tab |
| P5 | **V4-CONSOLE F2.** The DEK file gets `keyacl_windows.go`'s narrow ACL; the DPAPI scope question reopened | the key file's ACL asserted on Windows CI |

### P1 as built (2026-10-20)

As specified, with these details settled in the building. Read the first two
before P3.

- **Server verification uses the SCM, not the executable's path.** §3 says the
  console refuses unless the server "runs as SYSTEM from
  `%ProgramFiles%\NimbusBackup\NimbusBackupSVC.exe`". Two things make the path
  the wrong test: the installer lets the install folder be chosen
  (`WIXUI_INSTALLDIR`), so a fixed path refuses a legitimate install; and a
  standard user cannot open a SYSTEM process to read its path at all. The
  check is (1) the pipe's **owner is SYSTEM** (the service sets `O:SY`
  explicitly; a process that is not SYSTEM cannot name SYSTEM as owner), and
  (2) `GetNamedPipeServerProcessId` equals the process id the **Service
  Control Manager** reports for `NimbusBackup` (any interactive user may query
  a service's status; only an administrator can register or repoint one,
  which §7 already places beyond any design). `api.serviceCheck`.
- **Identification level and P3's impersonated writes conflict.** §3 has the
  console open the pipe with `SECURITY_IDENTIFICATION` so an impostor cannot
  act as it; §5.2 has the service write "while impersonating the caller".
  An identification-level token can be read and used for `AccessCheck`, but
  cannot be impersonated for a write. P1 does what §3 says. **P3 needs a
  decision (CJ).** Recommendation: keep identification level, and make §5.2's
  write as SYSTEM after `AccessCheck(destination's descriptor, caller's token,
  FILE_ADD_FILE | FILE_ADD_SUBDIRECTORY)` on a handle to the opened
  destination (so the check and the write are about the same object), with
  the file's owner set to the caller (SYSTEM holds `SeRestorePrivilege`). That
  keeps §5.2's two properties (the caller's own rights decide; files are the
  caller's) without handing an impersonation token to whatever is serving the
  pipe. The alternative, a second connection at `SECURITY_IMPERSONATION` after
  the first has verified the server, exposes an elevated window's
  administrator token (P4) to a server the console has not verified on that
  connection.
- **`GET /caller`** is a new route: the identity the service read for this
  connection (`user_sid`, `account`, `session_id`, `elevated`, `admin`; pid and
  image are log-only and not on the wire). P1's tests read the service's view
  through it, and P4's button needs it. User requirement; P2's table must list
  it. Allowed under the read-only lockdown.
- **Identity is read after the first byte.** Windows refuses
  `ImpersonateNamedPipeClient` until the client has written, so the server
  connection resolves its caller on its first read: before net/http has
  parsed anything, which is what §3 needs. The client's token
  (`TOKEN_QUERY | TOKEN_DUPLICATE`) is held for the connection's life and
  closed with it; one request per connection makes that the request's life.
- **Logging (§8, the local half).** Every request that is not a GET or HEAD is
  logged with account, SID, session, elevated, admin, client pid and image. The
  control-plane event fields (`caller_account`, `elevated`) are P3's, with the
  restores they describe.
- **No `http.Client` in the console.** `api.Client` dials a connection per
  request, sets one deadline over the whole exchange, `req.Write`,
  `http.ReadResponse`, and closes the connection with the body. Tests point it
  at an `httptest` server with `api.NewClientAt(url)`; a test gives
  `Handler()` an identity with `api.FixedCallerContext`.
  `TestOnlyTestsUseFixedCallerContext` fails if any non-test file in the
  repository uses either.
- **Failing to create the pipe fails the service's start.** `NimbusService.Start`
  creates the listener before it returns; the SCM sees the error. Outside
  Windows `ListenPipe` returns `ErrPipeUnsupported`, so the Linux service view
  (which exists for the linter and tests) does not start.
- **Removed:** `EnsureToken`, `restrictToOwners` and `acl_*.go`, the token
  transport and header, `api.TokenPath` and `api.DataDir` (the data directory
  is the service's own again, `gui/config.go`), port 18765, the Origin check,
  and `X-Nimbus-Token` from log redaction. On start the service deletes an
  `api-token` file an older version left in `ProgramData\NimbusBackup`.
- **D2, the WebView2 runtime.** On start the service reads the per-machine
  `pv` value Microsoft documents for detection; if there is no runtime at or
  above Wails v2.8.0's minimum (94.0.992.31), it downloads the Evergreen
  bootstrapper over HTTPS (redirects only to `*.microsoft.com`) into a folder
  only SYSTEM and Administrators can write, requires a valid Authenticode
  signature whose signer is **Microsoft Corporation**, and runs it with
  `/silent /install` (per-machine, since it runs as SYSTEM). A failure is
  logged and retried at the next start. The console sets Wails' missing-runtime
  message to point at the service rather than "contact your administrator".
- **`wails.json` never set the WebView2 strategy.** Its `"webview2":
  {"installmethod": ...}` key is not read by the Wails CLI (v2.8.0 or the
  v2.12.0 CI installs); the strategy is `wails build -webview2`, whose default
  is `download`. That is where the shipped console's downloader came from.
  Build GUI now passes `-webview2 error`, the inert key is removed, and
  `console/deps_test.go` reads the workflow and `scripts/local-gates.sh` to pin
  the flag and the tags (`TestTheShippedTagsAreTheBuildsTags`). The same file's
  `"windows": {"manifest": {"level": "requireAdministrator"}}` is also not
  read (the console runs `asInvoker`), which P4 depends on; left for P4 to
  remove.
- **The tests**, Windows CI (S8, `gui/api/pipe_windows_test.go`). One process
  is both ends, so the pipes carry the production descriptor with the test's
  user in SYSTEM's two places (derived from `pipeSDDL`, not restated), and
  `TestThePipeSecurityDescriptorIsTheSpecs` checks the production descriptor
  entry by entry. §10's "a client in the normal runner session gets
  `elevated=false`" is built as a token Windows cannot call elevated
  (Administrators deny-only, privileges removed), because hosted runners may
  run elevated; the runner's own token is checked against what the service
  saw as well. Remote refusal is tested for each defense alone, after a
  control pipe with neither proves the runner can reach its own pipes over
  SMB. Every refusal is paired with the same setup succeeding.
  `gui/localapi_test.go` checks that `Start` fails when the pipe does, and
  `gui/webview2_windows_test.go` the signature check (a Microsoft-signed .NET
  Framework executable, a changed signer name, an unsigned binary, a tampered
  one).
- **Lint.** `gui/api` is its own module and no lint pass reached it; Lint now
  runs it in its windows view (and `scripts/local-gates.sh` does). It needed
  only a package comment and two unchecked `Close`s.
- **Size.** Console **11,463,680 to 9,932,800** bytes stripped (local, the
  V4-CONSOLE §10 method), against §9's prototype 9,318,400; the console
  carries more binding code than the prototype, as C3 found. No
  `http.Transport`, `http.Client` or TLS handshake code remains (package
  initializers only). Service 11,073,024 to 11,123,200. Budget in Build GUI:
  **10,350,000**, not 9.6 MB, set as C3 set its own (shipped expected about
  1.5% above local, plus about 270 KB).

## 11. Open

- Whether stored descriptors exist for every entry in existing directory
  backups taken before the ACL side-car was added. If not, those backups show
  nothing to a user caller (5.1), and only an elevated window can browse them.
  That is the safe direction; P3 measures how many such backups exist on the
  bed.
- Whether a terminal-server deployment wants users to restore at all. Section 5
  makes it safe; org policy can still turn it off.
- **Before P3:** identification level against impersonated writes (section 10,
  "P1 as built", second item).

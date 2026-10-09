# v4: audit of the client GUI against the operating model

**Status (2026-10-09).** Specification only. No code has been changed. Written
against `v4_dev` at `226595c`; every line number below is `gui/frontend/src/App.jsx`
at that commit unless another file is named. Nothing here is built until CJ has
read it and answered section 7.

Companion documents: client `docs/V4-PIPELINE.md`, `docs/V4-RESTORE.md`;
NimbusControl `docs/V4-CLIENT-CONFIG.md`, `V4-AUDITABILITY.md`,
`V4-PBS-OPACITY.md`, `V4-ORG-SURFACE.md`, `V4-FIRST-ENROLLMENT.md`,
`V4-RUN-AUDIT.md` (1.3a, the handshake), `V4-UX.md`, `V4-FRONTEND.md`.

CJ's brief (2026-10-09): standalone clients are an edge case and the code this
was forked from was not built for a client-server model. Audit every GUI
process, and every component, field, button and text in it, for (1) whether it
belongs in a client-server product and (2) whether it is clear and consistent
in one design language built for that model.

---

## 0. How to read this document

**Verdicts.** `KEEP`, `CHANGE` (stays, but differently), `REMOVE` (gone in
every state), `HIDE-M` (absent when the machine is managed, present when
standalone), `STANDALONE` (the standalone-only control; same as HIDE-M, used
where the element exists only for that case).

**Rule names used in the "Spec" column.**

| Tag | Source |
|---|---|
| R1 | CJ ruling 2026-10-09: an organization's people never see PBS (server, datastore, namespace, snapshot path, which server) |
| R2 | CJ ruling: encryption is a switch for organization users; the level is superadmin-only |
| R3 | CJ ruling: a GUI-started backup asks the server first (`V4-RUN-AUDIT.md` 1.3a) |
| R4 | CJ ruling: the run's UUID is the "Run ID"; "job" means the schedule or definition |
| PIPE | client `docs/V4-PIPELINE.md` (the GUI renders, the service executes) |
| CFG | `V4-CLIENT-CONFIG.md` (managed vs local jobs, GUI lock, standalone toggle) |
| AUD | `V4-AUDITABILITY.md` (nothing only in an agent log) |
| OPQ | `V4-PBS-OPACITY.md` (the client never builds or asks for a PBS identifier) |
| ENR | `V4-FIRST-ENROLLMENT.md` (a new machine reports no errors) |
| UX | `V4-UX.md` section 8 (portal navigation rules and vocabulary) |
| PREF | CJ's standing preference: tiles, not tables with their own scrollbars |

**Evidence.** "Verified" means read in the code at the commit above. "Confirm on
the bed" means the code says so but only a Windows machine (agent 11 or 12) can
prove what the screen shows; those are listed in Appendix D and nothing is
built on them until confirmed.

---

## 1. What the audit found (ranked)

1. **The product still presents itself as the upstream vendor's product.**
   Header subtitle on every screen ("Backup client for Proxmox Backup Server -
   RDEM Systems"), a remote logo fetched from `nimbus.rdem-systems.com` on every
   About open, two order-storage links with tracking parameters, a copyright
   line, the MSI's Programs and Features help/about/contact links, the
   Wails author block. Section 3.11 and Appendix A.
2. **An organization user is shown PBS on the Servers, Backup, Restore and status screens.** The Servers
   tab is entirely PBS; the Restore tab starts from a PBS server picker and a
   typed backup-id; the locked status page prints the PBS host and datastore;
   snapshot cards, search hits and history rows print the backup-id; 31 catalog
   strings per language say "PBS" or "Proxmox". R1 forbids all of it.
3. **Managed jobs are invisible, and local-job writes bypass the lock.** The
   Backup tab lists only `scheduled_jobs.json`; the server's jobs live in
   `managed_jobs.json` and no GUI binding reads them (verified: `GetScheduledJobs`
   reads one file). A managed machine therefore shows a Scheduled-jobs list that
   omits its real jobs. Separately, `SaveScheduledJob`, `UpdateScheduledJob` and
   `DeleteScheduledJob` are Wails bindings that write the file from the GUI
   process (`scheduler.go` has no build tag), so the local-API allowlist in
   `readonly.go` never sees them: under `gui_read_only` the GUI hides the
   controls and nothing refuses the write (CFG 5.1, "the GUI hides, the service
   refuses").
4. **An enrolled machine is told to configure a PBS server.** `pbs_target` from
   the check-in is written to the legacy single-server fields
   (`applyPBSTargetFields`), but `ListPBSServers` returns only the multi-server
   map (`config.go:473`). So a healthy managed machine shows "Configure your
   first PBS server to start backups" and a Restore picker reading "No server
   configured". Confirm on the bed (Appendix D).
5. **The user types the PBS backup-id** (Backup tab 2508, Restore tab 2858) and
   the client falls back to the hostname. OPQ 3.4 ends both.
6. **The Start button and the Restore tab ignore the operating model.** Start posts directly, never
   consults `restrict_unmanaged_backups` (the status already carries it,
   `controlplane_glue.go:331`), and a refusal arrives as a toast that vanishes
   after five seconds. R3 adds the handshake and the refusal reason must be a
   state on screen, not a toast. The Restore tab never reads `policy_file_restore`
   (verified: the string does not occur in the front end), so it is drawn in full
   on a machine whose organization forbids file restore and the service refuses
   each call; CFG 5.1 and UX 8.1 rule 4 want the browser absent.
7. **Messages are ephemeral and mostly emoji.** 92 `showStatus` calls, every one
   auto-hiding after 5 s, rendered at the bottom of the active tab, below the
   fold on a normal window. About 210 emoji characters in `App.jsx`; nine native
   `confirm`/`alert` dialogs.
8. **Hardcoded French and duplicate catalog keys.** Ten live literals and thirteen dead fallbacks bypass
   `t()` (Appendix B); the Servers tab uses two parallel key sets for the same
   buttons (`test`/`testBtn`, `edit`/`editBtn`, `statusTesting`/`testing`...).
9. **Dead code from the fork.** Three unreferenced handlers (`handleSaveConfig`,
   `handleTestConnection`, `handleLoadConfigFile`), two unused states, a Servers
   table that can never render (it sits inside the `pbsServers.length === 0`
   branch and maps over that empty list), a mode test that can never be true
   (line 2564, see 3.6), and the dead tab name `'scheduled'` (line 2685).
10. **Auditability gaps (AUD 0).** Security-posture warnings exist only in the
    agent log and a GUI banner and are never reported; the local job-history file
    is a second history with different content from the server's; the split
    backup's retry prompts exist only on the console.

---

## 2. The context every screen is drawn from

Today each screen decides for itself what the machine is: `cpStatus` (polled
every 30 s), `readOnly` (a second, separate 30 s poll), `systemInfo` (read once
at mount), and several places read none of them. Result: no screen knows
whether it is managed.

**Proposal: one context object, computed once, passed down.** No new endpoint:
everything is already in `GetControlServerStatus()` and `IsReadOnly()`.

| Field | Source | Meaning |
|---|---|---|
| `serviceUp` | `GetSystemInfo().service_available` / poll failure | the local service answers |
| `managed` | `cpStatus.enrolled` (agent id > 0) | a control server owns this machine |
| `enrolling` | `cpStatus.configured && !enrolled` | URL set, token pending or in flight |
| `standalone` | `!cpStatus.configured` | no control server at all |
| `locked` | `IsReadOnly()` (`gui_read_only`) | status page only |
| `suspended` | `cpStatus.org_suspended` | org suspended; scheduled runs do not start |
| `restrictUnmanaged` | `cpStatus.restrict_unmanaged_backups` | org forbids work not authored by the server |
| `fileRestore` | `cpStatus.policy_file_restore` | org permits file restore on this machine |
| `policyKnown` | `cpStatus.policy_known` | a policy has arrived; absent means "unknown", never "permitted" |

States, and what the GUI is in each:

| State | Condition | The GUI is |
|---|---|---|
| Standalone | `standalone` | the full product, the edge case (section 5.2 of CFG; the service toggle still governs unmanaged work) |
| Enrolling | `enrolling` | a status screen with one sentence and no errors (ENR 0) |
| Managed | `managed` | the design target: a status and request front end |
| Managed, locked | `managed && locked` | the status page only (3.2) |
| Managed, suspended | `managed && suspended` | Managed, plus a banner; Start refused with the reason |
| Service down | `!serviceUp` | one sentence: the backup service is not answering, with what to do; nothing else renders |

`managed` and `standalone` are the two columns used in every table below.
Where a verdict says only one word it applies to both.

---

## 3. The processes

Each section: what the screen does today (element by element, with line
numbers), the verdict, then the two states side by side. "M" is managed,
"S" is standalone. Where the two are identical the table says "both".

### 3.1 First run and enrollment

What a new machine shows today. There is no first-run screen: the window opens
on the Servers tab (initial state, line 81) whatever the machine is.

| Moment | Today | Spec | Target |
|---|---|---|---|
| Installed from an MSI with a provisioning profile, before the first check-in | Servers tab: control-server block with the URL filled in and an empty token box; below it the PBS welcome box "Configure your first PBS server to start backups" | ENR 0: nothing is wrong, nothing may look wrong. R1: no PBS | **Enrolling** screen: one sentence, "This computer is connecting to your management server.", no fields, no error |
| During enrollment | Same, plus "Not enrolled" in the status line | ENR 0 | Same screen; if the attempt failed, the failure in plain words and the next step (3.4) |
| Enrolled, storage not yet assigned | PBS welcome box still shown; Restore picker reads "No server configured" (finding 4) | ENR R3: waiting is a state with neutral wording | Status screen, storage tile "Not assigned yet" (neutral dot), no error |
| Enrolled, devices not yet approved | `StorageIdentityPanel` titled by `status.error ? storageError : storageTitle` | ENR R3, R4 | Neutral "Awaiting device approval" until a real error exists (3.10) |
| Hand-installed, no profile (standalone) | Control-server block and PBS forms, nothing explains which is optional | CFG 5.2: standalone is supported but the edge case | Status screen with a "Set up this computer" panel offering the two paths: connect to a management server, or add your own backup storage (3.4, 3.5) |

| Element | Verdict | Why |
|---|---|---|
| Initial tab `servers` (line 81) | CHANGE | M and enrolling open on Status; S opens on Status too, with the setup panel |
| Welcome box (1980-1996) | HIDE-M, REMOVE its vendor link | Shown on an enrolled machine today (finding 4); wording "your first PBS server" violates R1 |
| "Don't have a PBS server yet? Order Nimbus Backup storage" + `utm_` link (1985-1994) | REMOVE | Third-party sales link carrying the app version; see 3.11 |

### 3.2 The status page, which is also the locked view

`StatusPanel.jsx` (310 lines) is already the right shape: three polled
endpoints, nothing executable. It is rendered instead of the tabs when the org
sets `gui_read_only` (line 1893). Its audit is mostly about R1 and wording.

| Element | Today | Spec | Verdict |
|---|---|---|---|
| Subtitle `hostname={config['backup-id']}` (1894) | Prints the PBS backup-id, not the computer's name | OPQ 3.4, R1 | CHANGE: the OS hostname (`hostname` state already holds it) |
| Connection tile "Control server" (229) | Host name, last check-in, raw `last_error` | R1 does not cover the management server; Q3 decides what it shows | CHANGE: connected / not connected and last check-in; host under a "details" disclosure |
| PBS tiles: title `p.name`, lines `p.host`, `p.datastore` (237-249) | Names the PBS server and datastore | **R1** | CHANGE: M shows one tile "Backup storage" with a state (reachable, not reachable, not checked yet) and a last-checked time; S shows name and host as today |
| `panelNoDestinations` empty text | Appears on a machine awaiting provisioning | ENR 0 | CHANGE: "Backup storage has not been assigned yet." (neutral) |
| Live run: name `run.job_name \|\| run.backup_id` (109) | Falls back to the PBS backup-id | OPQ, R4 | CHANGE: job name, else "Manual backup"; show the Run ID (short, copyable) |
| Live run: trigger prints the raw word (`schedule`, `portal`, `manual`...) (115) | Developer vocabulary | UX 2: "started from the portal" | CHANGE: translated phrases (Scheduled, Started from the portal, Started on this computer, At startup) |
| Live run: chunks "new / reused" (118) | Counter pair with no label meaning | none | CHANGE: keep under "Details"; the headline is percent, data, elapsed |
| Seven-day rollup keyed by `toISOString().slice(0,10)` (137) | Groups by **UTC** day, so an evening run in Texas lands on tomorrow | none | CHANGE: group by local day |
| Rollup shows the worst outcome of the day | Correct and documented (comment above `rollupByDay`) | none | KEEP |
| Detail table of 10 rows, result shows raw `r.error` (278-300) | Does not run NB codes through `localizeMessage`, which exists only inside `App` | A: errors say what happened | CHANGE: one shared message localizer (4.3); rows as tiles (PREF) |
| `panelManagedNotice` | Explains the lock | none | KEEP |
| Locked view renders no header controls (1893-1895) | A locked user cannot change language or text size | presentation only, no service call | CHANGE: render `HeaderControls` |
| Storage identity panel in the locked view | Present (`readOnly` prop) | CFG 5 | KEEP |

M versus S: identical except the storage tile (above). The locked view is a
managed state by definition; a standalone machine has no organization to lock it.

### 3.3 The frame: header, tabs, banners, header controls

| Element | Today | Spec | Verdict |
|---|---|---|---|
| Title `appTitle` with a shield emoji (1902) | "Nimbus Backup" | branding (CFG 6) | CHANGE: no emoji; name from branding, Q5 |
| Subtitle `appSubtitle` (1903) | "Backup client for Proxmox Backup Server - RDEM Systems" in all three languages | R1 (names PBS), vendor | REMOVE |
| Tabs `servers, backup, restore, about` as clickable `div`s (1935-1947) | Not focusable, no role | accessibility; UX 8.1 rule 4 | CHANGE: buttons with tab roles. M: Status, Back up, Restore (only if `fileRestore`), About. S: Status, Back up, Restore, Setup, About |
| Suspended banner (1910) | `<p class="card" role="alert">` | F-22 | KEEP; Start must also say why it refuses (3.6) |
| Security warnings banner (1927-1934) | English strings from Go, agent-log only | AUD 0 | CHANGE: 3.10 |
| Header controls: theme (auto/light/dark), accent swatches, text size, language with flag emoji | Persisted in `localStorage` | none | KEEP; language list shows names only (a flag is a country); default accent from branding when managed (Q5) |

### 3.4 Connecting to the control server

| Element | Today | Spec | Verdict |
|---|---|---|---|
| Section title and hint (1953-1954) | "Control server" | UX vocabulary | CHANGE: "Management server" |
| Status line: dot, host, "Connected", "Agent ID 12", last check-in, raw `last_error` (1955-1967) | Prints the numeric agent id and any transport error | ENR 0, AUD 0 | CHANGE: M shows connected / not connected and last check-in; id and host in "Details". A failure reads "Could not reach the management server. It will retry every N seconds." plus the code |
| URL, enrollment token, certificate fingerprint inputs and Save (1968-1976) | Always visible, also after enrollment | Provisioning supplies these (`provisioning.go`); typing them is the hand-install exception | M: REMOVE (they cannot be changed once enrolled; re-enrollment is the portal's, UX 11). Enrolling: read-only. S: shown in the setup panel, fingerprint under "Advanced" |
| Token cleared after Save (`setCpForm`, 255) | Good | | KEEP |
| `pending_enroll_token` returned by `ControlPlaneStatusMap` | Never read by the GUI | | USE: it is the "enrolling" signal |
| Status polled every 30 s; errors swallowed (`catch`, 244) | A stopped service looks like the last known state forever | | CHANGE: the context (section 2) records `serviceUp` and the screen says so |

### 3.5 The PBS server forms

Two copies of the same form (2050-2166 for no servers, 2229-2320 for some), plus
a server table that cannot render (2001-2047, inside the empty-list branch).

| Element | Today | Spec | M | S |
|---|---|---|---|---|
| The whole Servers tab | Nine fields, list, test/edit/default/delete | R1, CFG 2 (server-defined configuration is authoritative and read-only on the client); `pbs_target` already writes the target | **REMOVE** | CHANGE to "Backup storage" setup (below) |
| Server name | free text | | | KEEP |
| Server ID (+ placeholder `pbs-ssd`) | Developer identifier typed by the user | OPQ: no identifiers typed | | REMOVE (generate silently) |
| URL, Auth ID, Secret | `https://pbs-ssd.example.com:8007`, `backup@pbs!token-name`, uuid placeholder | | | KEEP; label "API token ID" and "Secret" |
| Datastore | free text | | | KEEP |
| Namespace (optional) | free text | | | KEEP under "Advanced" |
| Certificate fingerprint | free text; the TOFU flow can fill it | | | KEEP under "Advanced"; the pin prompt (`tofuConfirm`) becomes an in-app dialog |
| Description | free text | | | REMOVE |
| Secret placeholder `'•••••••• (laisser vide pour conserver le token actuel)'` (2101, 2280) | French literal, bypasses `t()` | i18n parity | | CHANGE |
| List columns: name, URL, datastore/namespace, status; buttons Test, Edit, Set default (star), Delete | Four emoji buttons per row | PREF | | CHANGE: one tile per server |
| "Multi-PBS" info box with the example `C:\ -> PBS big-data \| C:\Users -> PBS SSD` (2172-2174) | Describes per-path routing. No screen offers it: `StartBackup` takes no server argument and `ScheduledJob` has no server field (verified) | | | REMOVE |
| Two key sets for the same labels: `test`/`testBtn`, `edit`/`editBtn`, `statusTesting`/`testing`, `statusOnline`/`online`, `statusNotTested`/`untested`... | The dead branch uses one set, the live branch the other | | | MERGE |
| Delete uses native `confirm` (751), TOFU uses `window.confirm` (802) | Native dialogs | 4.2 | | CHANGE |
| "Tip: get your API token from the PBS interface" box (2163) | Useful for S | | | KEEP (S only) |

Open design question for S: keep several storage servers or exactly one (Q2).

### 3.6 A one-time backup (including the handshake, R3)

Today the Backup tab is one form that serves two different processes, "run now"
and "create a schedule", joined by a toggle labelled "Execution mode" (2363).
The portal separates them (UX section 1): *Run Backup Now* opens a dialog with
**Use a schedule** or **Custom**, and nothing starts until Confirm.

| Element | Today | Spec | Verdict |
|---|---|---|---|
| Backup type select, emoji options (2353-2359) | Folders / whole disks | UX 1: Image or Folders | KEEP, no emoji; labels "Folders" and "Whole disks (image)" |
| "Execution mode" segmented toggle: one-shot / scheduled (2363-2381) | Changes what the lower half of the form and the main button mean | UX 8.1 rule 1: a page answers one question | REMOVE: "Back up now" and "Scheduled jobs" become two sections (3.7) |
| Directories textarea, one path per line, typed (2437-2447) | Free text | `V4-JOB-TARGETS.md`: pick, do not type | CHANGE: folder picker (`PathPicker` exists, used today only for the restore destination) |
| Exclusions textarea | Folders only; hidden for image (correct, comment at 2426) | JOB-TARGETS section 4 | KEEP |
| Whole-disk warning box and disk checkboxes ("Boot drive", "Storage device", size) | Works | | KEEP |
| **Backup ID** input, placeholder "Leave empty to use hostname" (2508-2516) | The user types the PBS backup-id; empty falls back to the hostname | **OPQ 3.4, R1** | **REMOVE** (M and S: in S the service allocates it locally) |
| "Use VSS" checkbox | Jargon | | CHANGE wording: "Back up open files (VSS)" |
| Upload limit (Mbps) | Works | | KEEP |
| Exchange section, shown only when Exchange is installed | Works | | KEEP |
| VSS "administrator required" box, condition `systemInfo.mode === 'Standalone' && !is_admin` (2564) | **Can never be true.** `GetSystemInfo` returns `a.mode.String()`, which is "Service Mode", "Service Unavailable" or "In-Process (service)" (`api/mode.go:53`); the spec renamed the mode (PIPE 3.2). The engine runs in the service as LocalSystem, so the user's admin status is irrelevant | PIPE 3.2 | REMOVE |
| VSS "the service is available" info box, `service_available` (2571) | Appears whenever the service answers, i.e. always | | REMOVE |
| "Split the first backup" toggle (2579) and its hardcoded French flow (`executeSplitBackup`, 939-1024) | Issues one `StartBackup` per part with client-built backup-ids (`CreateBackupSplitPlan(backupDirs, backupID)`), confirm and alert text in French | OPQ 3.4 (no client-built ids), R3 (one request, one run), R4 | **REMOVE from M**; S: Q6 |
| Progress card (2594-2662): percent, bar, ETA, speed, elapsed, data, chunks, current folder, phase | Fed by the 3 s `/runs/active` poll (PIPE 3.5), the right design | PIPE 3.5 | KEEP; emoji removed; show the Run ID once known |
| Start button "Start backup" (2666-2672) | Posts `StartBackup(type, dirs, drives, excludes, backupId, vss, "")` with no server round trip | **R3** | CHANGE: see handshake below |
| Stop button, drawn only in one-shot mode, disabled at 0% (2675) | The poll shows any run's progress, so Stop works on a scheduled run in the default mode; switching the toggle to "scheduled" removes the button while the run continues | | CHANGE: Stop lives on the progress card of any running run (hidden when locked) |
| Re-run on a failed history row (2796-2809) | Copies directories, backup-id and VSS into the form and scrolls; not the type, exclusions or disks, and it does not switch tab | | CHANGE: "Run again" sends the same request (3.8) |

**The handshake, as the GUI sees it** (`V4-RUN-AUDIT.md` 1.3a). The wire and the
server half belong to the server session and the service's `StartBackup`; the GUI
owns what the person sees:

1. The person presses **Back up now** after choosing a job or a Custom definition.
2. Button becomes "Asking the server..." (no spinner emoji). The service makes
   the request and starts the run on the delivered path.
3. Accepted: the progress card appears with the Run ID. Same card as a scheduled
   or portal-started run (PIPE 3.5): there is no "as if".
4. Refused: a notice that **stays** (4.3), above the button, with the server's
   reason in the person's language: the organization does not allow backups
   started on a computer; the organization is suspended; this computer was
   deactivated; the server cannot be reached. The last is the only one with
   a way forward (the registry override, PIPE 3.1), and the notice says so only
   when the service reports the override is available.
5. Standalone: no handshake, the service starts the run directly.

When `restrictUnmanaged` is known to be set, **Custom is not drawn** and only
the managed jobs can be chosen (UX 8.1 rule 4). While the policy is not yet
known (`policyKnown` false) the screen shows the full choice and the service
decides; absent never means permitted for any other policy.

| | Managed | Standalone |
|---|---|---|
| Choice | A managed job, or a local job, or Custom, subject to `restrictUnmanaged` | A local job or Custom |
| Server round trip | Yes (R3) | None |
| Backup ID field | Gone | Gone (service allocates) |
| Refusal | Persistent notice with the server's reason | Service-side toggle refusal (`AllowUnmanagedBackups`), same notice style |

### 3.7 Scheduled jobs: local jobs and how managed jobs appear beside them

| Element | Today | Spec | Verdict |
|---|---|---|---|
| Whole list visible only in "scheduled" mode (2693) | Hidden behind the toggle | | CHANGE: its own section, always visible |
| Job tile: name, time, "Au demarrage" (hardcoded French, 2708), directories | Name is generated as `Backup <backupId>` (1066) and cannot be set; no last/next run though `LastRun`, `NextRun` and `Enabled` exist in `ScheduledJob` | CFG 2: managed jobs and local jobs are first-class and distinguished | CHANGE: name field; last run, next run; "Created on this computer" label |
| **Managed jobs** | **Not shown.** `GetScheduledJobs` reads `scheduled_jobs.json` only; managed jobs are in `managed_jobs.json`, held by the service (`managed_jobs.go`) | **CFG 2**, finding 3 | ADD: tiles labelled "Set by your organization", read-only, with schedule, last/next run and a **Back up now** button (goes through the handshake). No Edit, no Delete |
| Schedule: a time of day and "run at startup" (2385-2424) | Daily only | CFG 3: PVE calendar events replace `HH:MM`; the server derives expected intervals from the schedule a local job reports | CHANGE: presets that compile to a calendar expression (Daily at, Weekdays at, Every N hours); see Q8 |
| Edit: loads the job into the shared form (2722-2735) | Works; the form title says "Edit mode" in a warning box | | CHANGE: edit in place on the tile |
| Cancel edit: `setActiveTab('scheduled')` (2685) | **There is no such tab.** The four panes are servers, backup, restore and about, so every pane becomes inactive and the window shows the header and the tab strip over an empty body until a tab is clicked | | FIX (S1): stay on Back up |
| Delete (2738-2752) | No confirmation, immediate | UX 4 | CHANGE: in-app confirm naming the job |
| Writes: `SaveScheduledJob`, `UpdateScheduledJob`, `DeleteScheduledJob` are Wails bindings that write `scheduled_jobs.json` from the GUI process (`scheduler.go:102, 195, 242`) | Bypass the local API, so the lock's allowlist never sees them (finding 3). V4-CLIENT-CONFIG 5.3 also wants this file out of the logged-on user's reach | CFG 5.1 | **CHANGE**: the GUI binding calls `/jobs/create`, `/jobs/update`, `/jobs/delete` (they exist, `server.go:98-100`); the service writes. Then a locked GUI is refused by the allowlist with no new code |
| Job's PBS backup-id (`BackupID` in `ScheduledJob`, entered by the user) | Typed | OPQ 3.4: for a local scheduled job the client asks the server once per source when the job is saved (`backup-source`) | CHANGE: not a field; saving needs the server; offline save shows a reason |

| | Managed | Standalone |
|---|---|---|
| Managed jobs | Listed, read-only | Never exist |
| Local jobs | Only when the org does not set `restrict_unmanaged_backups` (Q4); otherwise the section is absent | Always |
| Local job ids | Server-assigned (OPQ 3.4) | Allocated by the service |

### 3.8 History and a run's detail (R4)

| Element | Today | Spec | Verdict |
|---|---|---|---|
| History card: last 6 entries from `GetJobHistory` (the local `job_history` file), scrolling box `maxHeight 400px`, refreshed every 10 s (2768-2810) | The service writes this file; the GUI re-reads it | PIPE 3.4: history is `/runs/recent`, served by the service. Two sources of truth today: the Backup tab reads the file, the status page reads `/runs/recent` | CHANGE: one source, `GetRecentRuns`; stop reading the file |
| Row: status emoji, `job.name`, `toLocaleString('fr-FR')` (2785), `localizeMessage(job.message)` | The date is French in every language | | CHANGE: locale of the chosen language |
| Row has no detail | No way to see duration, trigger, bytes, error text in full, or the Run ID | R4, AUD 4 ("Run ID" shown to everyone who may open the run) | ADD: a tile that opens to start/end, duration, how it started, data sent, result with what to do, and **Run ID** with a copy button, labelled "Run ID" and nothing else |
| Name of a custom run | `manual:HOSTNAME` for console-started runs | UX 1.2: "Manual backup" (portal) / "Manual backup (console)" | CHANGE with the server's naming |
| "Re-run" (failed only) | Fills the form | | CHANGE: "Run again" for any finished run; same request |
| "View in portal" | none | `dashboard_url` is already in the storage status | ADD, M only, when the URL is present (Q9) |

| | Managed | Standalone |
|---|---|---|
| Source | `/runs/recent` | `/runs/recent` |
| Run ID | Yes | Yes (the service mints it) |
| Portal link | When the dashboard URL is known | No |

### 3.9 Restore: finding a backup, browsing, searching, files, volumes, download

The engine is already in the service (`V4-RESTORE.md`); this is only what the
screen asks and shows. The unresolved question is whether the tab exists at all
on a managed machine (Q1). The rows give the verdict for the tab as it would
exist if it does.

| Element | Today | Spec | Verdict |
|---|---|---|---|
| Tab always drawn | `policy_file_restore` is never read (finding 6) | CFG 5.1, UX 8.1 rule 4 | CHANGE: M draws the tab only when `fileRestore` is true; S always |
| Beta box: "Restore is in beta" with checks and a cross (2824-2834) | Permanent banner | | REMOVE; the limitations are already carried by the disabled options and their reasons (`restoreMatrix`) |
| **PBS server picker** (2837-2855) | "Source PBS server" | **R1** | M REMOVE; S only if Q2 keeps several servers |
| **"Backup ID to restore"** input (2858-2866) | Typed, defaults to the hostname, French placeholder "hostname ou ID personnalise" (2863) | **OPQ 3.4, R1** | **REMOVE**. The page starts at "Backups of this computer": the service resolves the ids it knows for this computer's sources (a new read op beside the existing `/restore/query` ops) |
| "List snapshots" button | Explicit step | | REMOVE: the list is the first thing shown |
| Snapshot cards: `snap.time` with a camera emoji, `snap.backup_id`, type (3018-3040) | Prints the PBS backup-id on every card | R1, OPQ | CHANGE: date and time, "Whole disk image" or "Files and folders", job name |
| Search panel: name / regex / path, date range, "assemble missing" (2874-2990) | Crawls snapshots from a **prefix of the backup-id** (`SearchFiles(pbsID, hostPrefix, ...)`); "assemble missing" is index-building jargon | The browse design puts search on the server (`V4-BROWSE.md`, nimbus-browse decisions) | Q1; if the tab stays: CHANGE "assemble missing" to "Include backups not yet indexed (slower)" and search this computer only |
| Origin banner: original path, machine, saved at, client version, VSS (3049-3071) | Sidecar data; three `t(...) \|\| 'Source d'origine'` French fallbacks | | KEEP content; remove fallbacks |
| Tree with expand carets, checkboxes, folder sizes, shift/ctrl selection | Good | | KEEP |
| Volume flow: disk list -> partitions table -> Browse files -> directory table with breadcrumbs | Good function. Disk names are archive file names (`String(d).replace('.img.fidx','')`, 3109, 3123, 3178) | R1: "not a snapshot path" | CHANGE: "Disk 1 (boot)" from the reported disk list; partition table KEEP; `p.reason` shown as visible text (it already is) |
| Three native `window.alert` calls for volume errors (1567, 1585, 1624) and `confirm` for in-place restore (1351) and download-space (1754) | Native dialogs | 4.2 | CHANGE: in-app |
| Mode picker: restore in place / to another location; in-place warning; cross-computer checkbox | In-place disabled with a reason when the OS or sidecar rules it out (good) | | KEEP; the cross-computer checkbox: Q1 (restoring another computer's backup belongs in the portal) |
| Destination field + Browse; "Keep original folder structure" (hardcoded fr fallbacks 3370) | Works | | KEEP; remove fallbacks |
| Options: overwrite, timestamps, ACLs (beta tag), alternate streams, "Package as zip" | `Package as zip` silently changes the main button from Restore to Download without changing its label (3427, 3433-3440) | | CHANGE: two buttons, "Restore to folder" and "Download as zip" |
| Progress bar (`nc-taskbar`) with bytes, rate, ETA | Good | | KEEP |
| Bottom info box "Pick the source PBS server, enter a Backup ID, then list snapshots" (3468-3470) | Instructions for the old flow | | REMOVE |
| Scroll boxes: tree 360 px, search results 320 px, directory table | Inner scrollers | PREF is stated for portal pages | KEEP for trees and directory listings (a tree cannot be tiled); tell CJ in Q10 |

| | Managed | Standalone |
|---|---|---|
| Tab exists | Only if `fileRestore` (Q1) | Yes |
| Starts from | "Backups of this computer" | "Backups of this computer" |
| Server picker | None | Only if several servers are kept |
| Backup ID | None | None |
| Whole-machine restore | Not behind the file-restore gate (PIPE 3.1); the audit does not change it | Same |

### 3.10 Storage identity, security warnings, the suspended banner, Exchange

| Element | Today | Spec | Verdict |
|---|---|---|---|
| Storage identity panel: a `<details>` open only when `status.error` is set; summary `storageError` or `storageTitle` | A first-run machine that has not been approved yet has no error, but the server-side bug of 2026-10-08 set one (ENR section 1) | ENR R3/R4: waiting is neutral | CHANGE: three states, neutral "Awaiting device approval", approved, and error. Error only for the ENR R4 causes |
| `status.error` printed as the Go string | English only | ENR R2: the text is en/fr/es and says what happened | CHANGE: render by code through the shared localizer |
| Devices: model, `Number(size).toLocaleString()` followed by "B" (raw bytes), Evidence disclosure with device id, disk id, path, partition ids | Raw identifiers and byte counts | | CHANGE: model and formatted size; evidence stays collapsed |
| Approve button and checkboxes, hidden when `status.joined` | M approves in the portal (correct); S approves here | CFG, ENR | KEEP |
| "Dashboard" link built from `dashboard_url` + `/agents/<id>` | M only | | KEEP |
| Security-posture banner (`GetSecurityWarnings`, Windows only): two English sentences from Go | Logged once to the agent log and drawn as a banner. **Never reported to the server** (the only callers are `main.go:206` and the binding) | **AUD 0**: nothing only in an agent log; localization | CHANGE: report with the inventory so the machine page shows it; the GUI renders it from a code in the user's language |
| Suspended banner (`orgSuspendedBanner`) | Drawn in both the full and the locked views | F-22 | KEEP |
| Exchange section (aware, log truncation, log-mode readout) | Machine-specific, shown when installed | | KEEP; remove the emoji prefixes |

### 3.11 About

| Element | Today | Spec | Verdict |
|---|---|---|---|
| Logo `<img src="https://nimbus.rdem-systems.com/logo.webp">` (3482) | A network request to a third party every time the tab opens | privacy; vendor | REMOVE; bundled asset, or the branding logo (Q5) |
| "Order Nimbus Backup storage" button with `utm_source=NimbusGui&utm_campaign=version-<ver>` (3495) | Sales link, tracks versions | | REMOVE |
| Features list, Technology list ("Wails", "No GPU", "Modern interface") (3515-3535) | Marketing copy from the fork | | REMOVE |
| `copyright`: "(c) 2026 RDEM Systems" and a link to `nimbus.rdem-systems.com` (3541-3542) | Upstream vendor | | CHANGE per Q5 |
| `basedOn`: "Based on proxmoxbackupclient_go by tizbac"; `techStack` | The project is GPL-3 and derived from that work | license: notices must be kept | **KEEP** as a Credits and licenses line; nothing else on this tab is required |
| Version | Shown | | KEEP, add build and the computer's name |
| (new) Support contact | none | CFG 6 branding | ADD from branding when managed (Q5) |

Not on screen but in the product (Appendix A): Programs and Features help, about
and contact links in the MSI, the Wails author block, the release-notes
generator, the fyne-cross app id.

### 3.12 The tray menu

`tray.go`: tooltip, **Show window**, a disabled **Status** line, **Quit**. No
specification exists (`V4-UX.md` section 9); this is therefore a proposal, not a
reading of one.

| Element | Today | Verdict |
|---|---|---|
| Show window | Works | KEEP |
| Status line (disabled item) | Carries the storage-fault latch (`updateStorageTray`) | CHANGE: last result and next run ("Last backup OK, 2 h ago") from `/runs/recent`; the storage fault stays, in neutral words until it is a real error |
| **Quit** | Closes the window process. In a backup product the word reads as "stop backing up"; the service keeps running | CHANGE: "Close (backups keep running)" |
| Language | Follows the GUI language (`SetTrayLanguage`) | KEEP |

Q7 asks whether anything else belongs in the tray.

### 3.13 Every status and error message

92 `showStatus` call sites. By kind:

| Kind | Examples | Problem | Rule (4.3) |
|---|---|---|---|
| Progress as a toast | `searchingSnapshots`, `loadingSnapshotContents`, `volumeReadingParts`, `splitAnalyzing` | Replaced by the next toast; vanishes at 5 s | Inline state on the control that started it |
| Success | `statusServerAdded`, `scheduleCreated`, `snapshotsFound` ("n found"), `entriesLoaded` | Noise: the list that appeared is the confirmation | Drop where the screen already shows the result |
| Error with raw text | `showStatus('error ' + err)` at 914, 1117, 1133, 1159, 1190, 1411 | Go error string, English, no next step. Only strings containing `[NB-nnnn]` are localized (`localizeMessage`, 642) | Always a code, localized text, what to do |
| "No runtime" | `errNoRuntime` guard in 13 handlers | Reachable only in a browser without Wails (development); dead for a user | Guard once at startup, not per handler |
| French | `Backup n/m termine`, `echoue`, `Voulez-vous reessayer` in the split flow (996-1019) | Bypasses `t()` | Gone with the split flow (Q6) or keyed |
| Placement | Rendered at the bottom of the active pane (2344, 2815, 3472) | Below the fold at normal window sizes | Next to the control |

Keep: `localizeMessage` and the `[NB-xxxx] :: detail` contract with `errcodes.go`;
it is the right mechanism and only needs to be shared (StatusPanel, the storage
panel and the security banner do not use it).

---

## 4. The design language

Rules every screen follows. They are proposals CJ can veto one by one; each
exists because a specific defect above needs it. Wording is taken from the
portal (`V4-UX.md`); step S4 first diffs this table against the portal's
translation files and reports mismatches rather than guessing.

### 4.1 One name per thing

| Thing | The word | Never |
|---|---|---|
| The computer the person is at | **This computer** | machine, host, client, agent |
| The server that manages it | **Management server** | control server, control plane, NimbusControl |
| Where backups go (M) | **Backup storage** | PBS, Proxmox, datastore, namespace, server |
| Where backups go (S) | Backup storage server (name, address) | PBS (except in the one field that asks for a PBS token) |
| A schedule or definition | **Job** ("Nightly") | task, plan, schedule (as a noun for the whole) |
| One execution of a job | **Run**; its UUID is the **Run ID** | Backup Job ID, backup job, execution |
| Starting one now | **Back up now**; a custom one is a **Manual backup** | one-shot, execution mode, start |
| A dated, restorable copy | **Backup** ("Backup of 9 Oct 2026, 02:00") | snapshot (portal staff pages may keep it) |
| Whole-disk copy | **Whole disk (image)** | machine backup, volume, fidx |
| A job from the server / from here | **Set by your organization** / **Created on this computer** | managed / local, unmanaged |
| The list of past runs | **History** | journal, log |

### 4.2 Buttons and dialogs

* Verb and object, sentence case: *Back up now, Save job, Delete job, Restore to
  folder, Download as zip, Cancel.* One primary button per section.
* A button that changes meaning with a toggle is two buttons (the Restore/zip
  case, 3.9).
* Destructive actions open an in-app dialog that names the thing ("Delete job
  Nightly?") with the consequence. No native `confirm` or `alert` (nine today).
  Pinning a certificate shows the fingerprint in an in-app dialog.

### 4.3 Status, notices and errors

* Three kinds: **progress** (inline on the control that started it, no timer),
  **result** (stays until the next action or dismissal), **error** (stays until
  dismissed).
* An error says **what happened, what to do, and the code**: *"Could not start
  the backup. Your organization does not allow backups started on this
  computer. Ask your administrator. [NB-xxxx]"*. Never a raw exception string,
  never English in a French window.
* Placed next to the control, not at the bottom of the pane.
* A waiting state is not an error (ENR 0): neutral dot, neutral words.
* One localizer (`localizeMessage`, moved out of `App`) used by every component.

### 4.4 Hidden versus disabled

* If the machine's state or the organization's policy makes a control
  impossible and the person cannot change that, it is **not rendered** (UX 8.1
  rule 4, PIPE 4, CFG 5.1). The service refuses it anyway.
* A control is **disabled with its reason shown as visible text** only when the
  person can fix the cause (no folder chosen; no disk ticked; in-place restore
  not possible for this backup). A tooltip is never the only place a reason lives.
* Policy defaults differ and the GUI follows them: `restrict_unmanaged_backups`
  unknown means permitted; `file_restore` unknown means denied (PIPE 3.1).

### 4.5 No emoji

None in labels, buttons, headings or messages. State is a word plus a coloured
dot or bar. The language menu lists names, not flags. (Today: about 210 emoji
characters in `App.jsx`; the portal uses none.) Exception: none.

### 4.6 Time, size, locale

One `format` module: dates and times in the language selected in the GUI (not
`'fr-FR'`), local day boundaries, byte sizes through one function (no raw
`Number(bytes).toLocaleString() + ' B'`).

### 4.7 Identifiers

The GUI never shows or asks for a PBS identifier (R1, OPQ). It shows the
**Run ID** (copyable) and the computer's OS hostname. A support person needs
nothing else from the screen.

### 4.8 Layout

Tiles, not tables with their own scrollbars (PREF). Trees and directory
listings keep their inner scroller because a tree cannot be tiled. Tabs are
buttons. Every control has a label bound to it.

### 4.9 Translation

en, fr and es in parity (the build already fails otherwise). No
`t('key') || 'literal'` (thirteen dead fallbacks today, Appendix B) and no literal
text: the audit script is extended to catch both (S4).

---

## 5. Build order

Smallest and safest first. Each step is one pull request into `v4_dev`; CJ merges.
**Every pull request targets the next MSI**, the one that already carries ENR
R1/R2/3.4, OPQ 3.4 (server-assigned backup-ids), the handshake and the removal of
the client's snapshot-comment stamp. Each PR body says "MSI: next" and names
the server PR it depends on, if any. Spec, then tests, then code; docs move with
the code.

There is no JavaScript test runner in the front end today (`package.json` has
only `dev`, `build`, `preview`, `i18n-audit`). Rather than add one, steps from S2
on make each process a props-only component, so `node --test` with
`react-dom/server` (already installed) can render any state from fixtures. No new
dependency.

| # | Pull request | Tests (written first) | Depends on |
|---|---|---|---|
| S1 | **Dead code and two bugs.** Delete `handleSaveConfig`, `handleTestConnection`, `handleLoadConfigFile`, `selectedPBSID`, `restoreProgress`, the unreachable server table (2001-2047), the two VSS boxes (2564, 2571). Fix the cancel-edit tab (2685). No other change | `npm run build` (runs the i18n audit); a script that lists the `t()` keys used before and after: only removals expected | none |
| S2 | **Split `App.jsx` by process**: Status, Backup, Jobs, History, Restore, Setup, About; hooks `useMachineContext`, `usePolling`; modules `format`, `messages`. No behavior change | Per-component render fixtures; rendered-text snapshot per component taken from the S1 build, must be identical | S1 |
| S3 | **Remove the upstream vendor** from the GUI, installer and metadata (Appendix A); keep the GPL credit line | A build step failing on `rdem`, `utm_` or any external image or link in `src/`, allowlisting only the credit URL; a CI grep over `installer/` for the same | Q5 for the replacement text; without it, ship the product name and no contact |
| S4 | **Translation hygiene**: live French literals to keys; merge the duplicate key sets; delete the thirteen dead `\|\| 'literal'` fallbacks; local dates and sizes through `format`; extend `i18n-audit.mjs` to reject `\|\| '...'`, non-ASCII string literals in JS, and native dialogs | Fixture files in `scripts/` with one failing sample per new rule | S2 |
| S5 | **Context, Status and the six states** (section 2): Enrolling, Service-down, Standalone setup panel; Status as the default tab; the 3.2 changes (hostname, one "Backup storage" tile, trigger words, local days, shared localizer, header controls in the locked view) | Render tests for each state. **R1 gate:** fixtures contain a PBS host, datastore, namespace and backup-id; for every managed state the rendered text may contain none of them. Local-day grouping unit test with a 22:00 America/Chicago run. Storage fixture of finding 4 renders no welcome box | S2 |
| S6 | **Setup**: Servers tab removed for managed; standalone gets the reduced form (Q2), one list, no Server ID or Description, no Multi-PBS box | M renders no Setup; S form field list asserted; finding-4 fixture | S5, Q2 |
| S7 | **Notices**: one component replaces the 92 `showStatus` calls; no emoji; native dialogs replaced; errors carry codes | The audit script rejects `showStatus(`, `window.confirm`, `window.alert` and emoji outside an allowlist; render test: an error notice has no timer | S4 |
| S8 | **Back up and Jobs**: "Back up now" and "Scheduled jobs" as two sections; Backup ID field and mode toggle removed; managed jobs listed read-only; local-job writes only through the local API; Stop on the progress card | Go: every `/jobs*` write route refused while locked (extend `readonly_test.go`); a file-set test that the GUI build links no job writer (the PIPE 3.1 method); new read route returns the managed set; render tests for M, S and `restrictUnmanaged` | S5; the read route (section 6) |
| S9 | **Handshake states**: asking, accepted, each refusal reason as a persistent notice | A fake local API per refusal reason; render per state | S8; the server's `backup-request` and the service `StartBackup` PR |
| S10 | **Restore**: "Backups of this computer", policy gate, no backup-id or server picker, new cards, "Disk 1 (boot)", two buttons, no beta box | R1 gate over restore fixtures (cards, search hits, disk names); tab absent when `fileRestore` false or unknown (M); two buttons asserted | Q1; OPQ 3.4 server-assigned ids; the "list this computer's backups" read op |
| S11 | **History and run detail** with the Run ID, single source `/runs/recent`, "Run again", portal link | The label "Run ID" present and "Backup Job ID" absent in all three languages; render of detail tile | S8 |
| S12 | **About, tray, storage panel, security warnings**: neutral first approval, localized storage error, security warning as a reported code | Tray text table test; storage states render | Server field for the warning report; ENR R2 wording |
| S13 | **Leftover documents** (Appendix C) | Link check over the remaining docs | none |

---

## 6. What this needs from outside the GUI

Listed so the server session and the service work are not duplicated.

| Need | Where | For |
|---|---|---|
| `organization_name` (and, if Q5 says so, branding: logo, product name, accent, support contact) in `ControlPlaneStatusMap` / the check-in | server + `controlplane_glue.go` | 3.1, 3.4, 3.11 |
| A read-only route that returns the managed job set (`currentManagedJobs()`), added to the `readonly.go` allowlist only if the locked view ever needs it (it does not: keep it off) | `gui/api` | 3.7 |
| Local-job writes go through `/jobs/create`, `/jobs/update`, `/jobs/delete`; GUI binding bodies replaced | `gui/scheduler.go` | 3.7, finding 3 |
| A restore read op: backups of this computer, resolved by the service from server-assigned source ids | `restore_service.go`, `/restore/query` | 3.9 |
| Server-assigned backup-ids and `backup-source` | server + service | 3.6, 3.7, 3.9 |
| The handshake (`backup-request`) and refusal reasons as codes | server + service `StartBackup` | 3.6 |
| Security-posture warnings reported with the inventory, as codes | `secposture_windows.go`, inventory | 3.10 |
| Storage-identity error as a code with en/fr/es text (ENR R2) | `storage_state.go` | 3.10 |
| Run detail fields the tile shows (bytes uploaded, ended_at, error code) already in `RunsResponse`; confirm `bytes_uploaded` | `gui/api/runs.go` | 3.8 |

---

## 7. Questions for CJ

Decisions the audit cannot make. Each has the recommendation I would build if
you do not object; nothing in S5 to S12 starts on the dependent steps until
answered.

1. **Does the Restore tab exist on a managed machine, or is restore the portal's
   job?** *Recommend:* it exists only when the organization permits file restore,
   starts at "Backups of this computer", offers file, folder and volume restore
   and download, and has no search and no restoring another computer's backup
   (server-side browse owns both). If you say portal only, S10 shrinks to a
   removal for managed.
2. **What may a standalone install show of PBS?** *Recommend:* a reduced form
   (name, address, API token ID and secret, datastore; namespace and fingerprint
   under Advanced) and **exactly one** storage server in the GUI, since no screen
   can route a path to a second one today. The Go map of servers stays until a
   later cleanup.
3. **Does a managed machine show the control server's address?** *Recommend:* the
   organization's name, connected or not, and the last check-in; address and
   agent id under a "Details" disclosure for support calls. Needs the
   organization name in the status (section 6).
4. **Do local jobs exist on a managed machine by default?** *Recommend:* only
   while the organization does not set `restrict_unmanaged_backups`, shown as a
   secondary section under the managed jobs. The key's default permits, so by
   default they would exist; say if enrollment should flip that default
   (server policy, not GUI).
5. **Branding.** What does About show for a branded installation, and what
   replaces "(c) RDEM Systems"? *Recommend:* logo, product name and support
   contact from branding when managed; a plain "Nimbus Backup" with no company
   line otherwise; keep "Based on proxmoxbackupclient_go by tizbac" as a credits
   line (GPL-3). Does the agent receive branding at all today? I found no field.
6. **The "split the first backup" feature.** It runs several backups with
   client-built ids and French prompts. *Recommend:* remove it. If large first
   backups still need it, specify it as a service feature on server-assigned
   source ids.
7. **The tray.** There is no specification (`V4-UX.md` section 9). *Recommend:*
   Open, a status line (last result, next run), and "Close (backups keep
   running)". Nothing else.
8. **Local schedule input.** *Recommend:* presets (daily at, weekdays at, every
   N hours) that compile to the PVE calendar expression CFG 3 already decided
   on, rather than asking a person to type `mon..fri 02:00`.
9. **A "View in portal" link on a run**, when the dashboard URL is known.
   *Recommend:* yes, managed only.
10. **Design-language defaults** (section 4): no emoji anywhere; trees and
    directory listings keep an inner scroller, everything else is tiles; the
    vocabulary table in 4.1. Say which to change.

---

## Appendix A: the nine leads from the handoff, verified

| # | Lead | Result |
|---|---|---|
| 1 | Upstream vendor in the product | **Confirmed and wider.** Logo fetched from `nimbus.rdem-systems.com` (3482); order-storage CTA with `utm_` (3495); first-run link with `utm_` (1987); footer link (3542); `chooseBackupUrl` in all three catalogs (`translations.js` 348, 852, 1356); `appSubtitle` on every screen; `copyright` "(c) 2026 RDEM Systems"; `installer/wix/Product.wxs` 152-155 (Programs and Features help, about, update-info and contact link to the vendor's site, repository and e-mail); `gui/wails.json` author block; `gui/main.go:183` website line; `.github/workflows/build-and-release.yml` release body (1580-1631) and attestation comment (1499); `build_gui_windows_docker.sh:21` app id; `installer/DEPLOYMENT.md`; `docs/RESTORE_GUIDE.md`; `CHANGELOG.md`; `MULTI_PBS_USER_GUIDE.md`. Not all of these are in the GUI; S3 covers GUI, installer and metadata, S13 the documents. The workflow's own repository path `rdemsystems/NimbusBackupClient` is CJ's to decide |
| 2 | PBS forms contradict R1 | **Confirmed**, 3.5. On an enrolled machine the form is not used: `pbs_target` writes the legacy single-server fields, and the Servers tab reads the multi-server map, which is empty (finding 4) |
| 3 | "Backup ID" contradicts opacity | **Confirmed** at 2508 (Backup) and 2858 (Restore), plus the job tile, history rows, snapshot cards and search hits |
| 4 | Restore starts from PBS concepts | **Confirmed**, 3.9 |
| 5 | "Execution mode" and the mode name | **Confirmed.** The label is wrong (two processes in one toggle) and `systemInfo.mode === 'Standalone'` (2564) can never be true: the string comes from `ExecutionMode.String()` (`api/mode.go:53`) which returns "Service Mode", "Service Unavailable" or "In-Process (service)". The initial state `mode: 'Standalone'` (lines 84 and 535) is the only place the word survives |
| 6 | Dead tab name | **Confirmed**, line 2685; also the only caller of `setActiveTab` with a name outside the four panes |
| 7 | Emoji carry meaning | **Confirmed**, about 210 characters; status colors always have words beside them, so the rule is removable without loss |
| 8 | Leftover fork documents | Appendix C |
| 9 | One 3,556-line component | **Confirmed**; S2 |

New findings beyond the nine: managed jobs invisible and direct job writes
(3.7); `policy_file_restore` ignored (3.9); the Multi-PBS box promises routing
that does not exist (3.5); the Start button ignores `restrict_unmanaged_backups`
(3.6); StatusPanel prints PBS host and datastore and the backup-id as the
computer's name (3.2); UTC day grouping (3.2); locked view has no header
controls (3.2); security warnings never reported (3.10); two history sources
(3.8); the Servers tab's duplicate key sets (3.5).

## Appendix B: text outside the catalog

Live (shown to a user, bypass `t()`):

| Line | Text |
|---|---|
| 996, 1001, 1006-1007, 1019 | Split-backup progress and retry prompt in French |
| 2101, 2280 | Secret placeholder in French |
| 2708 | "Au demarrage" |
| 2785 | `toLocaleString('fr-FR')` |
| 2863 | Placeholder "hostname ou ID personnalise" |
| `HeaderControls.jsx:106` | Language names with flag emoji (names are correct; flags go) |

Dead fallbacks (the key exists in fr, en and es; the literal can never show,
and the audit script does not look for it): 1583, 1617, 3064, 3066, 3068, 3082,
3085, 3299, 3310, 3366, 3369, 3370, 3381.

Go strings shown in the GUI: security-posture warnings (`secposture_windows.go`),
the storage-identity `error`, and `errNoPBSServer` / "serveur PBS ... introuvable"
(`config.go:485, 403`, French inside an English error).

## Appendix C: leftover documents at the repository root

| File | Lines | Verdict |
|---|---|---|
| `MULTI_PBS_GUIDE.md` | 348 | REMOVE: French implementation guide for a feature the GUI does not use (Q2) |
| `MULTI_PBS_USER_GUIDE.md` | 266 | REMOVE: end-user guide to Multi-PBS, vendor links |
| `README.fr.md` | 36 | Compare with `README.md` (141 lines, updated 2026-09-08); keep only if maintained in parity, otherwise REMOVE |
| `RELEASE_NOTES.md` | 37 | CHANGE or REMOVE: "Status and notes" page from the fork; `CHANGELOG.md` carries history |
| `MSI_BUILD_GUIDE.md` | 258 | KEEP after a read for current paths; check the vendor references |
| `MSI_UNINSTALL_TEST.md` | 175 | KEEP; French; test notes for the uninstall dialog |
| `ARCHITECTURE.md` | 958 | KEEP (updated 2026-09-28); add the GUI context and states once S5 lands |
| `CHANGELOG.md` line 104 | | The statement "machine (image) backups don't yet report the real backup-time" is flagged stale in the handoff; check against `gui/` before editing |

S13 reads each in full before deleting anything; this table is from titles, line
counts and dates only.

## Appendix D: not proven by reading code

Needs a Windows machine from the bed (agent 11 in org 2, agent 12 in org 3, which
CJ operates). Nothing is built on these until confirmed.

1. An enrolled machine shows the first-run "Configure your first PBS server" box
   and "No server configured" in the Restore picker (finding 4; from
   `applyPBSTargetFields` and `ListPBSServers`).
2. Whether a logged-on user's GUI can actually write `scheduled_jobs.json` on a
   locked machine. The code path bypasses the allowlist; the file's DACL decides
   whether the write succeeds (CFG 5.3 notes the file inherits ProgramData's
   default today).
3. Whether `ListSnapshots("")` on an enrolled machine resolves the target from
   the legacy fields (`EffectivePBS`) or fails with `errNoPBSServer`.
4. What the tray status item displays at idle.
5. `gui/go.sum`: the client `docs/V4-STATUS.md` heading says `gui/` has none; not
   checked, since no Go change is part of this document.

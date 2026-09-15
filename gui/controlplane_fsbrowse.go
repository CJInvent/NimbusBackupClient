package main

// controlplane_fsbrowse.go — the agent side of LIVE FILESYSTEM browsing for
// the portal's directory-job target picker.
//
// NimbusControl docs/V4-JOB-TARGETS.md section 2. A directory job's targets
// are chosen by browsing the machine, with the same multi-selection shape the
// restore browser already uses, because a free-text path field for a machine
// the operator is not sitting at cannot be right more often than their memory
// of it.
//
// Command contract (payload -> result, all JSON):
//
//	list_dir {path} -> {path, entries: [...], truncated}
//	                   path empty = this machine's ROOTS, which are this
//	                   machine's lettered drives
//
// There is deliberately no separate "list the disks" command. The image job's
// disk picker is fed by the check-in INVENTORY (V4-JOB-TARGETS.md section
// 1.1) and an empty-path list_dir already returns the drives as roots, so a
// third path to the same fact would be a third thing to keep in step.
//
// Entries use the same compact keys as the image browser, for the same
// reason (the server caps command result bodies):
//	p=path  d=is_dir  s=size  m=mtime(unix)
//
// THIS IS METADATA ONLY. list_dir reads names, sizes and times; it never
// returns file contents. Reading a customer's file bytes is the restore path,
// which has its own authorization and its own audit trail.
//
// UNTAGGED, unlike the image browse handlers: this needs no engine, no PBS
// credentials and no volume parser -- just the local filesystem -- so there
// is nothing for a `service`-only build to own. The check-in loop that
// receives commands runs in the service either way.
//
// WHERE AUTHORIZATION LIVES. The operator-level gate is server-side
// (AGENT_MANAGE on the org, tenancy pinned, every list_dir audited with the
// path requested): the agent has no notion of who is holding the portal.
// What the agent owes this path is confinement -- an absolute local path
// under this machine's own roots, nothing over the network, no device paths,
// metadata only -- which is what cpBrowsePath enforces below.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"controlplane"
)

// cpListDirCap bounds one directory listing. A folder with more children than
// this returns its first entries and truncated=true; the picker says so.
// Smaller than the image browser's cap because this listing is for CHOOSING a
// folder, not for finding one file among a scanned volume.
const cpListDirCap = 5000

// cpHandleFSBrowseCommand answers list_dir. Returns ok=false for commands it
// does not own, so cpHandleCommand falls through.
func (a *App) cpHandleFSBrowseCommand(cmd controlplane.Command) (controlplane.CommandResult, bool) {
	switch cmd.Command {
	case "list_dir":
		raw, _ := cmd.Payload["path"].(string)
		if strings.TrimSpace(raw) == "" {
			roots, err := cpBrowseRoots()
			if err != nil {
				return cpErr(err.Error()), true
			}
			return controlplane.CommandResult{OK: true, Result: map[string]interface{}{
				"path": "", "entries": roots, "truncated": false,
			}}, true
		}
		path, err := cpBrowsePath(raw)
		if err != nil {
			return cpErr(err.Error()), true
		}
		entries, truncated, err := cpListDir(path)
		if err != nil {
			return cpErr(err.Error()), true
		}
		return controlplane.CommandResult{OK: true, Result: map[string]interface{}{
			"path": path, "entries": entries, "truncated": truncated,
		}}, true
	}
	return controlplane.CommandResult{}, false
}

// cpBrowsePath confines a requested path to THIS machine's own storage.
//
// Refused, and why each one matters:
//
//   - a relative path. It would resolve against the service's working
//     directory, which is not a place anybody chose, and the job would store
//     a target that means something different to every process that reads it.
//   - a UNC path (\\server\share). That is someone else's machine, reached
//     with this service's credentials. Browsing it through this agent would
//     be using the agent as a proxy onto the network it sits in -- and a
//     network share as an image or directory target is a separate decision
//     nobody has made here.
//   - a device path (\\.\PhysicalDrive0, \\?\...). Raw device access is the
//     image engine's business, and it is not browsable as a filesystem.
//
// The path is lexically cleaned, so `C:\Users\..\Windows` becomes
// `C:\Windows` before anything reads it: the portal should see the path it
// will store, not the one it typed.
func cpBrowsePath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", errors.New("no path given")
	}
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") {
		return "", errors.New("network and device paths cannot be browsed from here: " + raw)
	}
	if runtime.GOOS == "windows" {
		if !cpHasDriveLetter(p) {
			return "", errors.New("a path must name a drive on this machine, e.g. C:\\Users: " + raw)
		}
	} else if !strings.HasPrefix(p, "/") {
		return "", errors.New("a path must be absolute: " + raw)
	}
	return filepath.Clean(p), nil
}

// cpHasDriveLetter reports the C:\ or C:/ shape. A bare "C:" is deliberately
// NOT accepted: on Windows it means "the current directory on C", which is
// process state, not a place.
func cpHasDriveLetter(p string) bool {
	if len(p) < 3 {
		return false
	}
	c := p[0]
	isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	return isLetter && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// cpBrowseRoots is what an empty path lists: the places a browse can START.
// The machine's lettered drives, which are the roots an operator recognizes
// and the only ones a job's target can be under.
func cpBrowseRoots() ([]map[string]interface{}, error) {
	if runtime.GOOS != "windows" {
		return []map[string]interface{}{{"p": "/", "d": true, "s": int64(0), "m": int64(0)}}, nil
	}
	disks, err := ListPhysicalDisks()
	if err != nil {
		return nil, fmt.Errorf("enumerate disks: %w", err)
	}
	seen := map[string]bool{}
	out := make([]map[string]interface{}, 0, 4)
	for _, d := range disks {
		for _, l := range d.Letters {
			root := strings.ToUpper(strings.TrimRight(strings.TrimSpace(l), `:\`)) + `:\`
			if seen[root] {
				continue
			}
			seen[root] = true
			out = append(out, map[string]interface{}{
				"p": root, "d": true, "s": int64(0), "m": int64(0),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["p"].(string) < out[j]["p"].(string)
	})
	return out, nil
}

// cpListDir reads one directory's immediate children.
//
// Directories first, then files, each alphabetically -- the order a person
// browsing for a folder to back up needs, rather than the order the
// filesystem happens to return.
//
// An entry whose metadata cannot be read is still LISTED, without a size: a
// folder the service cannot stat is a folder that exists, and dropping it
// would show the operator a directory that is missing something they can see
// in Explorer.
func cpListDir(path string) ([]map[string]interface{}, bool, error) {
	items, err := os.ReadDir(path)
	if err != nil {
		return nil, false, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir() != items[j].IsDir() {
			return items[i].IsDir()
		}
		return strings.ToLower(items[i].Name()) < strings.ToLower(items[j].Name())
	})
	truncated := false
	if len(items) > cpListDirCap {
		items = items[:cpListDirCap]
		truncated = true
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		e := map[string]interface{}{
			"p": filepath.Join(path, it.Name()),
			"d": it.IsDir(),
			"s": int64(0),
			"m": int64(0),
		}
		if info, ierr := it.Info(); ierr == nil {
			if !it.IsDir() {
				e["s"] = info.Size()
			}
			e["m"] = info.ModTime().Unix()
		}
		out = append(out, e)
	}
	return out, truncated, nil
}

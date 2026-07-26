//go:build windows
// +build windows

package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/getlantern/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var (
	trayInitialized = false
	menuShow        *systray.MenuItem
	menuStatus      *systray.MenuItem
	menuQuit        *systray.MenuItem
	trayLang        = "fr" // default matches the frontend's i18n default

	// trayExitedCh closes once systray's own message loop has confirmed the
	// icon is removed (see onExit's doc comment). Callers that are about to
	// os.Exit wait on this — with their own timeout and their own exit code,
	// so a crash path can never be masked into reporting success.
	trayExitedCh   = make(chan struct{})
	trayExitedOnce sync.Once
)

// markTrayExited signals trayExitedCh exactly once. Called from onExit.
func markTrayExited() {
	trayExitedOnce.Do(func() { close(trayExitedCh) })
}

// waitForTrayExit blocks until the tray icon is confirmed removed or timeout
// elapses, whichever is first. If the tray was never initialized this simply
// waits out the full timeout — callers should check trayInitialized first to
// skip that wait entirely.
func waitForTrayExit(timeout time.Duration) {
	select {
	case <-trayExitedCh:
	case <-time.After(timeout):
	}
}

// trayText returns the tray strings for a language. Kept in Go (not the
// frontend i18n files) because the tray exists even when no window/webview
// is open. Keys: show, showTip, status, statusTip, quit, quitTip, tooltip.
// NOTE: the tray MENU's colors are drawn by Windows itself and cannot be
// themed by the app; language is ours, chrome is the OS's.
func trayText(lang string) map[string]string {
	switch lang {
	case "en":
		return map[string]string{
			"show": "🖥️ Show window", "showTip": "Open the Nimbus Backup interface",
			"status": "📊 Backup status", "statusTip": "View scheduled backup status",
			"quit": "❌ Quit", "quitTip": "Close Nimbus Backup",
			"tooltip": "Nimbus Backup — scheduled backups active",
		}
	case "es":
		return map[string]string{
			"show": "🖥️ Mostrar ventana", "showTip": "Abrir la interfaz de Nimbus Backup",
			"status": "📊 Estado de las copias", "statusTip": "Ver el estado de las copias programadas",
			"quit": "❌ Salir", "quitTip": "Cerrar Nimbus Backup",
			"tooltip": "Nimbus Backup — copias programadas activas",
		}
	default: // fr
		return map[string]string{
			"show": "🖥️ Afficher la fenêtre", "showTip": "Ouvrir l'interface Nimbus Backup",
			"status": "📊 État des sauvegardes", "statusTip": "Voir l'état des sauvegardes planifiées",
			"quit": "❌ Quitter", "quitTip": "Fermer Nimbus Backup",
			"tooltip": "Nimbus Backup — sauvegardes planifiées actives",
		}
	}
}

// SetTrayLanguage is Wails-bound: the frontend calls it at startup and on
// every language change so the tray follows the GUI language live.
func (a *App) SetTrayLanguage(lang string) {
	if lang != "fr" && lang != "en" && lang != "es" {
		return
	}
	trayLang = lang
	if !trayInitialized {
		return
	}
	tt := trayText(lang)
	systray.SetTooltip(tt["tooltip"])
	if menuShow != nil {
		menuShow.SetTitle(tt["show"])
		menuShow.SetTooltip(tt["showTip"])
	}
	if menuStatus != nil {
		menuStatus.SetTitle(tt["status"])
		menuStatus.SetTooltip(tt["statusTip"])
	}
	if menuQuit != nil {
		menuQuit.SetTitle(tt["quit"])
		menuQuit.SetTooltip(tt["quitTip"])
	}
}

// SetupSystemTray initializes the system tray icon and menu
func (a *App) SetupSystemTray() {
	if trayInitialized {
		return
	}

	writeDebugLog("Setting up system tray")

	// Setup tray in goroutine to avoid blocking
	go func() {
		systray.Run(onReady(a), onExit)
	}()

	trayInitialized = true
}

func onReady(a *App) func() {
	return func() {
		// Set tray icon from embedded PNG data (icon.go)
		systray.SetIcon(TrayIconData)
		systray.SetTitle("Nimbus Backup")
		tt := trayText(trayLang)
		systray.SetTooltip(tt["tooltip"])

		// Add menu items — strings follow the GUI language (SetTrayLanguage).
		menuShow = systray.AddMenuItem(tt["show"], tt["showTip"])
		systray.AddSeparator()

		menuStatus = systray.AddMenuItem(tt["status"], tt["statusTip"])
		menuStatus.Disable() // For display only

		systray.AddSeparator()
		menuQuit = systray.AddMenuItem(tt["quit"], tt["quitTip"])

		// Handle menu item clicks
		go func() {
			for {
				select {
				case <-menuShow.ClickedCh:
					writeDebugLog("Tray: Show window clicked")
					// Show the main window
					runtime.WindowShow(a.ctx)
					runtime.WindowUnminimise(a.ctx)
				case <-menuQuit.ClickedCh:
					writeDebugLog("Tray: Quit clicked")
					// Ask systray to remove the icon (Shell_NotifyIcon NIM_DELETE)
					// and request Wails shutdown in parallel.
					systray.Quit()
					runtime.Quit(a.ctx)
					// Exit once the icon is confirmed gone, or after a bound in
					// case the message loop is ever stuck — imperceptible to the
					// user either way, and it's what actually closes the race
					// that used to leak the icon on every Quit.
					go func() {
						waitForTrayExit(3 * time.Second)
						writeDebugLog("Tray: exiting after Quit")
						os.Exit(0)
					}()
				}
			}
		}()

		writeDebugLog("System tray initialized")
	}
}

// onExit is called by the systray library's own Windows message loop, and
// ONLY after it has already called Shell_NotifyIcon(NIM_DELETE) on our icon —
// it fires from the WM_DESTROY/WM_ENDSESSION handler, right after nid.delete().
// That makes it the one point where we know the icon is actually gone, so any
// process-ending os.Exit should wait for this (via waitForTrayExit) rather
// than racing it on a fixed timer.
//
// This is what most of the leaked/duplicate tray icons CJ is seeing come from:
// getlantern/systray never sets NOTIFYICONDATA's GUID (NIF_GUID), so Windows
// has no stable identity for the icon across restarts — it only ever gets
// removed if THIS process calls Shell_NotifyIcon(NIM_DELETE) before exiting.
// Every os.Exit that skipped this callback (the old fixed 2s timer here, and
// any crash) left a ghost entry in the notification area, which Windows keeps
// until it's hovered or explorer.exe restarts — hence icons piling up over a
// dev session of repeated kills/restarts. A Task Manager kill, debugger stop,
// or power loss is still unrecoverable in userspace; no app-level fix changes
// that. Restarting explorer.exe (or a reboot) clears whatever has already
// leaked.
func onExit() {
	writeDebugLog("System tray exiting (icon removed)")
	markTrayExited()
}

// MinimizeToTray hides the window and minimizes to tray
func (a *App) MinimizeToTray() {
	writeDebugLog("Minimizing to tray")
	runtime.WindowHide(a.ctx)
}

// ShowFromTray shows the window from tray
func (a *App) ShowFromTray() {
	writeDebugLog("Showing from tray")
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
}

// UpdateTrayTooltip updates the tray icon tooltip (e.g., with next backup time)
func (a *App) UpdateTrayTooltip(message string) {
	if !trayInitialized {
		return
	}
	systray.SetTooltip(fmt.Sprintf("Nimbus Backup - %s", message))
}

// attemptTrayCleanupBeforeCrash gives a panic a brief, bounded chance to
// remove the tray icon before main's recover() calls os.Exit(1). Called from
// main.go, which is cross-platform (!service, no OS constraint) — this
// Windows implementation is paired with a no-op in tray_stub.go so that call
// site doesn't need to know whether a tray exists on this platform.
//
// systray.Quit() only posts a window message, so this cannot hang the crash
// handler; the bound is short and deliberately does NOT change the caller's
// exit code — a crash must still report failure even when the tray happens
// to clean up in time.
func attemptTrayCleanupBeforeCrash() {
	if !trayInitialized {
		return
	}
	systray.Quit()
	waitForTrayExit(300 * time.Millisecond)
}

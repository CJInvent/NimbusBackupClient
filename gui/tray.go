//go:build windows
// +build windows

package main

import (
	"fmt"
	"os"
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
)

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
					// Quit systray first
					systray.Quit()
					// Request Wails shutdown
					runtime.Quit(a.ctx)
					// Force exit after short delay if graceful shutdown doesn't work
					go func() {
						time.Sleep(2 * time.Second)
						writeDebugLog("Force exit after timeout")
						os.Exit(0)
					}()
				}
			}
		}()

		writeDebugLog("System tray initialized")
	}
}

func onExit() {
	writeDebugLog("System tray exiting")
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

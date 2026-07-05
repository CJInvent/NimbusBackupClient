package main

// Phase 4a: agent-side alerting. When a backup fails, the process that ran it
// (the service, or a standalone GUI) sends an email with the failure message
// and the tails of the current service/backup logs, so the operator gets the
// diagnostics without touching the machine. This is the first control-plane
// building block: a future control server replaces/augments SMTP as a sink,
// but the hook and log-collection points stay the same.
//
// Deliberately conservative: alerts are best-effort (never block or fail a
// backup), fire only when both an SMTP host and a recipient are configured,
// and the SMTP password rides the existing Phase 2/3 encryption at rest.

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// alertBackupFailure fires a best-effort failure alert in the background.
func (a *App) alertBackupFailure(message string) {
	cfg := a.config
	if cfg == nil || cfg.SMTPHost == "" || cfg.AlertEmail == "" {
		return
	}
	// A rejected duplicate start (concurrency guard) is operator feedback,
	// not a backup failure - do not page anyone for a double-click.
	if strings.Contains(message, "[NB-2004]") {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				writeDebugLog(fmt.Sprintf("[Alerts] panic while sending alert: %v", r))
			}
		}()
		hostname, _ := os.Hostname()
		subject := fmt.Sprintf("Nimbus Backup FAILED on %s", hostname)
		body := fmt.Sprintf(
			"Backup failure on %s at %s\n\n%s\n\n===== Recent log excerpts =====\n%s",
			hostname,
			time.Now().Format("2006-01-02 15:04:05 MST"),
			message,
			collectLogTails(60),
		)
		if err := sendAlertEmail(cfg, subject, body); err != nil {
			writeDebugLog(fmt.Sprintf("[Alerts] failed to send failure alert: %v", err))
			return
		}
		writeDebugLog(fmt.Sprintf("[Alerts] failure alert sent to %s", cfg.AlertEmail))
	}()
}

// sendAlertEmail delivers via SMTP. Port 465 uses implicit TLS; anything else
// dials plain and upgrades with STARTTLS when the server offers it. Server
// certificates are always verified against the configured host.
func sendAlertEmail(cfg *Config, subject, body string) error {
	port := cfg.SMTPPort
	if port == "" {
		port = "587"
	}
	addr := net.JoinHostPort(cfg.SMTPHost, port)
	from := cfg.SMTPFrom
	if from == "" {
		from = cfg.SMTPUsername
	}
	if from == "" {
		return fmt.Errorf("smtp sender unknown: set smtp_from or smtp_username")
	}

	var client *smtp.Client
	tlsCfg := &tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12}
	if port == "465" {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("smtp tls dial failed: %w", err)
		}
		client, err = smtp.NewClient(conn, cfg.SMTPHost)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp handshake failed: %w", err)
		}
	} else {
		conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
		if err != nil {
			return fmt.Errorf("smtp dial failed: %w", err)
		}
		client, err = smtp.NewClient(conn, cfg.SMTPHost)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp handshake failed: %w", err)
		}
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsCfg); err != nil {
				_ = client.Close()
				return fmt.Errorf("smtp starttls failed: %w", err)
			}
		}
	}
	defer func() { _ = client.Close() }()

	if cfg.SMTPUsername != "" && cfg.SMTPPassword != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPHost)); err != nil {
			return fmt.Errorf("smtp auth failed: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail-from failed: %w", err)
	}
	if err := client.Rcpt(cfg.AlertEmail); err != nil {
		return fmt.Errorf("smtp rcpt-to failed: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data failed: %w", err)
	}
	msg := strings.Join([]string{
		"From: " + from,
		"To: " + cfg.AlertEmail,
		"Subject: " + subject,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")
	if _, err := w.Write([]byte(msg)); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp send failed: %w", err)
	}
	return client.Quit()
}

// collectLogTails returns the last n lines of the freshest service-*.log and
// backup-*.log in the log directory. Globbing (instead of the fixed accessors)
// keeps this correct in both the -gui and -service processes.
func collectLogTails(n int) string {
	var out strings.Builder
	for _, prefix := range []string{"service-", "backup-"} {
		if path := newestLog(prefix); path != "" {
			out.WriteString(fmt.Sprintf("--- %s (last %d lines) ---\n", filepath.Base(path), n))
			out.WriteString(tailFile(path, n))
			out.WriteString("\n")
		}
	}
	if out.Len() == 0 {
		return "(no log files found)"
	}
	return out.String()
}

func newestLog(prefix string) string {
	matches, err := filepath.Glob(filepath.Join(logDir, prefix+"*.log"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		fi, err1 := os.Stat(matches[i])
		fj, err2 := os.Stat(matches[j])
		if err1 != nil || err2 != nil {
			return false
		}
		return fi.ModTime().After(fj.ModTime())
	})
	return matches[0]
}

// tailFile reads at most the last 64KB of a file and returns its last n lines.
func tailFile(path string, n int) string {
	f, err := os.Open(path) // #nosec G304 -- path comes from our own log directory glob
	if err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", filepath.Base(path), err)
	}
	defer f.Close()
	const window = 64 * 1024
	st, err := f.Stat()
	if err != nil {
		return fmt.Sprintf("(cannot stat %s: %v)", filepath.Base(path), err)
	}
	off := int64(0)
	if st.Size() > window {
		off = st.Size() - window
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", filepath.Base(path), err)
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // first line is likely cut mid-way
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

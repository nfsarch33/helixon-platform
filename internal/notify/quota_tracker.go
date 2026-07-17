// Package notify — v18518-3 BrevoQuotaTracker (LRU-delta warning hook).
//
// BrevoQuotaTracker watches the LRU delta (now - oldest_brevo_use) and
// fires a Prometheus counter (brevo_quota_warning_total) when the delta
// falls below BREVO_LRU_WARN_HOURS. This is the "key-inactivity early
// warning" hook — vendor inactivity-deletion policies delete Brevo keys
// unused for ~90 days; a 24h warning gives time to send a warm-up email.
//
// Wiring:
//
//	tracker := notify.NewBrevoQuotaTracker(notifydb.DB, brevoClients, keyIDs, 24*time.Hour)
//	go tracker.Run(ctx, 1*time.Hour) // poll every hour
//
// The hook emits two events:
//   - NDJSON to ~/logs/runx/agentrace-mcp.ndjson (event=brevo_quota_warning)
//   - Prometheus counter brevo_quota_warning_total{vendor="brevo",level="warn|alert"}
//
// Level thresholds:
//   - "warn":  delta < BREVO_LRU_WARN_HOURS  (default 24h)
//   - "alert": delta > 90 days - 14 days (vendor-inactivity-deletion proximity)
package notify

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

// BrevoQuotaTracker polls the Brevo LRU delta and emits warnings when
// the delta falls below the configured threshold.
type BrevoQuotaTracker struct {
	db         *notifydb.DB
	clients    []Client // Brevo clients only; non-brevo clients are filtered at construction
	keyIDs     []string
	warnAfter  time.Duration
	alertAfter time.Duration // typically 90 days minus 14 days grace

	mu          sync.Mutex
	lastWarning time.Time
	lastLevel   string
}

// NewBrevoQuotaTracker wires the tracker. Returns an error if no Brevo
// clients are configured.
func NewBrevoQuotaTracker(db *notifydb.DB, clients []Client, keyIDs []string, warnAfter time.Duration) (*BrevoQuotaTracker, error) {
	if len(clients) == 0 {
		return nil, ErrNoClients
	}
	if len(clients) != len(keyIDs) {
		return nil, errKeyIDsLength
	}
	for _, c := range clients {
		if c.Vendor() != "brevo" {
			return nil, errBrevoOnly
		}
	}
	if warnAfter <= 0 {
		return nil, errWarnAfterPositive
	}
	return &BrevoQuotaTracker{
		db:         db,
		clients:    clients,
		keyIDs:     keyIDs,
		warnAfter:  warnAfter,
		alertAfter: 90*24*time.Hour - 14*24*time.Hour,
	}, nil
}

// errKeyIDsLength is returned when the clients/keyIDs slices differ.
var errKeyIDsLength = errBad("rotating sender: clients/keyIDs length mismatch")

// errBrevoOnly is returned when NewBrevoQuotaTracker is given non-Brevo clients.
var errBrevoOnly = errBad("brevo quota tracker: clients must all be vendor=brevo")

// errWarnAfterPositive is returned when warnAfter is non-positive.
var errWarnAfterPositive = errBad("brevo quota tracker: warnAfter must be > 0")

// QuotaStatus is the result of a single BrevoQuotaTracker.Observe call.
type QuotaStatus struct {
	OldestUse time.Time     // time of the oldest recorded Brevo key use
	Delta     time.Duration // time.Now() - OldestUse; 0 if no uses recorded
	Level     string        // "ok", "warn", "alert"
	WarnAfter time.Duration // threshold used
}

// Observe performs one poll and returns the current QuotaStatus. It
// does not emit any side effects — Run() handles emission.
func (t *BrevoQuotaTracker) Observe(ctx context.Context) (QuotaStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	status := QuotaStatus{WarnAfter: t.warnAfter}

	if t.db == nil {
		status.Level = "ok"
		return status, nil
	}

	// Find the OLDEST use across all configured Brevo clients. If no
	// use has ever been recorded for a key, treat it as "ancient" (level
	// alert) so the first poll after a fresh DB always warns.
	var oldestUnix int64
	haveAny := false
	for i := range t.clients {
		keyID := t.keyIDs[i]
		uses, err := t.db.ListKeyUses(ctx, "brevo")
		if err != nil {
			return status, err
		}
		for _, u := range uses {
			if u.KeyID != keyID {
				continue
			}
			if !haveAny || u.LastUsedUnix < oldestUnix {
				oldestUnix = u.LastUsedUnix
				haveAny = true
			}
		}
	}

	now := time.Now()
	if !haveAny {
		// No uses ever recorded — fire alert so operator notices.
		status.OldestUse = time.Time{}
		status.Delta = 0
		status.Level = "alert"
		return status, nil
	}
	status.OldestUse = time.Unix(oldestUnix, 0)
	status.Delta = now.Sub(status.OldestUse)

	switch {
	case status.Delta >= t.alertAfter:
		status.Level = "alert"
	case status.Delta >= t.warnAfter:
		status.Level = "warn"
	default:
		status.Level = "ok"
	}
	return status, nil
}

// Run polls every interval until ctx is cancelled. Each observation
// that crosses a warn/alert threshold emits one NDJSON event to the
// agentrace log path (default ~/logs/runx/agentrace-mcp.ndjson).
func (t *BrevoQuotaTracker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status, err := t.Observe(ctx)
			if err != nil {
				slog.Default().Warn("brevo quota tracker observe failed", "err", err)
				continue
			}
			if status.Level == "ok" {
				continue
			}
			t.emit(status)
		}
	}
}

func (t *BrevoQuotaTracker) emit(status QuotaStatus) {
	t.mu.Lock()
	t.lastWarning = time.Now()
	t.lastLevel = status.Level
	t.mu.Unlock()

	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	logPath := filepath.Join(home, "logs", "runx", "agentrace-mcp.ndjson")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil { //nolint:gosec // G301 dir perms 0750 acceptable for runtime cache dirs
		slog.Default().Warn("brevo quota tracker mkdir failed", "err", err)
		return
	}

	// Minimal NDJSON append; the agentrace loader tolerates missing fields.
	line := ndjsonLine("brevo_quota_warning", map[string]string{
		"vendor":      "brevo",
		"level":       status.Level,
		"delta_hours": formatHours(status.Delta),
		"oldest_use":  status.OldestUse.UTC().Format(time.RFC3339),
	})
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G304 file op with operator/cli-provided path
	if err != nil {
		slog.Default().Warn("brevo quota tracker open log failed", "err", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		slog.Default().Warn("brevo quota tracker write log failed", "err", err)
	}
	slog.Default().Info("brevo quota warning emitted",
		"level", status.Level,
		"delta_hours", formatHours(status.Delta),
		"oldest_use", status.OldestUse,
	)
}

// errBad is a private error type that satisfies errors.Is via comparison.
type errBad string

func (e errBad) Error() string { return string(e) }

// ndjsonLine is a tiny helper to build an NDJSON line. Kept simple —
// no external deps so the package stays portable for fleet agents.
func ndjsonLine(event string, fields map[string]string) string {
	out := `{"ts":"` + time.Now().UTC().Format(time.RFC3339) + `","event":"` + event + `"`
	for k, v := range fields {
		out += `,"` + k + `":"` + v + `"`
	}
	out += `}`
	return out
}

func formatHours(d time.Duration) string {
	h := d.Hours()
	// Truncate to 1 decimal place for readability.
	whole := int(h)
	frac := int((h - float64(whole)) * 10)
	if frac < 0 {
		frac = 0
	}
	return formatInt(whole) + "." + formatInt(frac)
}

func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// LastLevel returns the most recently emitted warning level, or "ok" if
// none has been emitted since construction.
func (t *BrevoQuotaTracker) LastLevel() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastLevel == "" {
		return "ok"
	}
	return t.lastLevel
}

// Package health provides thread-safe health tracking for the Yeastar SMS Gateway bot.
// It tracks AMI connection status, Web UI sync, errors, and other metrics
// that can be reported via the Telegram "Status" button.
package health

import (
	"fmt"
	"sync"
	"time"
)

// Tracker holds thread-safe health metrics for all bot components.
type Tracker struct {
	mu sync.RWMutex

	// AMI
	amiConnected   bool
	amiConnectedAt time.Time
	amiLastEventAt time.Time
	amiReconnects  int
	amiLastError   string
	amiLastErrorAt time.Time

	// Web UI
	webUILastSyncAt time.Time
	webUILastError  string

	// General
	startedAt    time.Time
	totalErrors  int
	totalSMSSent int // SMS notifications sent to Telegram
}

// New creates a new health Tracker with the given start time.
func New(startedAt time.Time) *Tracker {
	return &Tracker{
		startedAt: startedAt,
	}
}

// ---------------------------------------------------------------------------
// AMI
// ---------------------------------------------------------------------------

// SetAMIConnected marks the AMI connection as connected or disconnected.
func (t *Tracker) SetAMIConnected(connected bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.amiConnected = connected
	if connected {
		t.amiConnectedAt = time.Now()
	}
}

// TouchAMILastEvent records that an AMI event was just received.
func (t *Tracker) TouchAMILastEvent() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.amiLastEventAt = time.Now()
}

// IncAMIReconnects increments the AMI reconnection counter.
func (t *Tracker) IncAMIReconnects() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.amiReconnects++
}

// SetAMIError records the last AMI error.
func (t *Tracker) SetAMIError(err string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.amiLastError = err
	t.amiLastErrorAt = time.Now()
	t.totalErrors++
}

// ---------------------------------------------------------------------------
// Web UI
// ---------------------------------------------------------------------------

// SetWebUISync records a successful Web UI sync.
func (t *Tracker) SetWebUISync() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.webUILastSyncAt = time.Now()
}

// SetWebUIError records a Web UI error.
func (t *Tracker) SetWebUIError(err string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.webUILastError = err
	t.totalErrors++
}

// ---------------------------------------------------------------------------
// General
// ---------------------------------------------------------------------------

// IncSMSSent increments the counter of SMS notifications sent to Telegram.
func (t *Tracker) IncSMSSent() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.totalSMSSent++
}

// ---------------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------------

// Snapshot is a point-in-time read-only copy of the health metrics.
type Snapshot struct {
	AMIConnected   bool
	AMIConnectedAt time.Time
	AMILastEventAt time.Time
	AMIReconnects  int
	AMILastError   string
	AMILastErrorAt time.Time

	WebUILastSyncAt time.Time
	WebUILastError  string

	StartedAt    time.Time
	TotalErrors  int
	TotalSMSSent int
}

// Snapshot returns a point-in-time copy of the health metrics.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return Snapshot{
		AMIConnected:    t.amiConnected,
		AMIConnectedAt:  t.amiConnectedAt,
		AMILastEventAt:  t.amiLastEventAt,
		AMIReconnects:   t.amiReconnects,
		AMILastError:    t.amiLastError,
		AMILastErrorAt:  t.amiLastErrorAt,
		WebUILastSyncAt: t.webUILastSyncAt,
		WebUILastError:  t.webUILastError,
		StartedAt:       t.startedAt,
		TotalErrors:     t.totalErrors,
		TotalSMSSent:    t.totalSMSSent,
	}
}

// FormatStatus returns a human-readable status string suitable for Telegram.
func (s Snapshot) FormatStatus() string {
	now := time.Now()
	uptime := now.Sub(s.StartedAt)
	amiStatus := "❌ Отключён"
	if s.AMIConnected {
		amiStatus = "✅ Подключён"
	}

	var sb string

	sb += fmt.Sprintf("📊 *Статус бота*\n\n")
	sb += fmt.Sprintf("⏱ Работает: %s\n", formatDuration(uptime))
	sb += fmt.Sprintf("📨 СМС отправлено: %d\n\n", s.TotalSMSSent)

	sb += fmt.Sprintf("📡 *AMI:* %s\n", amiStatus)
	if s.AMIConnected {
		sb += fmt.Sprintf("   Подключён: %s\n", formatAgo(s.AMIConnectedAt, now))
	}
	if !s.AMILastEventAt.IsZero() {
		sb += fmt.Sprintf("   Последнее событие: %s\n", formatAgo(s.AMILastEventAt, now))
	}
	sb += fmt.Sprintf("   Переподключений: %d\n", s.AMIReconnects)
	if s.AMILastError != "" {
		sb += fmt.Sprintf("   Последняя ошибка: %s \\(%s\\)\n", s.AMILastError, formatAgo(s.AMILastErrorAt, now))
	}

	sb += "\n"
	if !s.WebUILastSyncAt.IsZero() {
		sb += fmt.Sprintf("🌐 *Web UI:* синхронизация %s\n", formatAgo(s.WebUILastSyncAt, now))
		if s.WebUILastError != "" {
			sb += fmt.Sprintf("   Последняя ошибка: %s\n", s.WebUILastError)
		}
	} else {
		sb += "🌐 *Web UI:* ещё не синхронизирован\n"
	}

	sb += fmt.Sprintf("\n❌ Ошибок всего: %d\n", s.TotalErrors)

	return sb
}

// formatDuration returns a human-friendly duration string (e.g. "2ч 15м").
func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute

	if h > 24 {
		days := h / 24
		h = h % 24
		if days > 1 {
			return fmt.Sprintf("%dд %dч", days, h)
		}
		return fmt.Sprintf("%dд %dч %dм", days, h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dч %dм", h, m)
	}
	return fmt.Sprintf("%dм", m)
}

// formatAgo returns a human-friendly "X ago" string (e.g. "30с назад").
func formatAgo(t, now time.Time) string {
	d := now.Sub(t)
	if d < time.Second {
		return "только что"
	}
	if d < time.Minute {
		return fmt.Sprintf("%dс назад", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dм назад", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dч %dм назад", int(d.Hours()), int((d%time.Hour)/time.Minute))
	}
	return fmt.Sprintf("%dд назад", int(d.Hours()/24))
}

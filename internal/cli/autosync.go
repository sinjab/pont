package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/sinjab/pont/internal/store"
	"github.com/sinjab/pont/internal/whatsapp"
)

const autoSyncThreshold = 24 * time.Hour
const autoSyncTimeout = 30 * time.Second

// autoSyncRetryAfter bounds how soon a TIMED-OUT quick-sync is retried:
// an unconfirmed sync must not buy a full 24h of auto-sync suppression.
const autoSyncRetryAfter = 10 * time.Minute

// shouldAutoSync checks if an auto-sync is needed based on last sync time.
func shouldAutoSync(db *store.DB) bool {
	if NoAutoSync() {
		return false
	}

	lastSync, err := db.GetLastSyncTime()
	if err != nil {
		return false
	}

	// Never synced or sync is stale
	if lastSync.IsZero() {
		return true
	}

	return time.Since(lastSync) > autoSyncThreshold
}

// formatTimeSince returns a human-readable duration since the given time.
func formatTimeSince(t time.Time) string {
	if t.IsZero() {
		return "never"
	}

	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}

// maybeAutoSyncWithClient performs a sync if needed, on an ALREADY-CONNECTED
// client (WithConnection path). The WithDB read-path variant was removed in
// P1 (Oct 11 2026): scripted reads are pure local queries now — `sync` is
// the explicit refresh.

// maybeAutoSyncWithClient performs a sync if needed, using an existing client connection.
func maybeAutoSyncWithClient(client *whatsapp.Client, db *store.DB) error {
	if !shouldAutoSync(db) {
		return nil
	}

	lastSync, _ := db.GetLastSyncTime()
	fmt.Fprintf(os.Stderr, "Auto-syncing (last sync: %s)...\n", formatTimeSince(lastSync))

	return performQuickSync(client, db)
}

// performQuickSync waits for sync events with a timeout.
func performQuickSync(client *whatsapp.Client, db *store.DB) error {
	select {
	case <-client.SyncComplete:
		fmt.Fprintln(os.Stderr, "Sync complete.")
		// Only a confirmed-complete sync earns a fresh timestamp.
		if err := db.SetLastSyncTime(time.Now()); err != nil {
			return fmt.Errorf("failed to update sync time: %w", err)
		}
	case <-time.After(boundedWaitTimeout(autoSyncTimeout)):
		fmt.Fprintln(os.Stderr, "Sync timeout (continuing with available data).")
		// Completion was never confirmed — recording a fresh sync time
		// here would suppress auto-sync for a full 24h on unconfirmed
		// data (the consult-flagged freshness lie). Instead stamp a
		// synthetic time (now - threshold + retryAfter) so the 24h gate
		// re-arms and the next attempt comes after autoSyncRetryAfter.
		// The pre-sync "last sync" message reads this field BEFORE the
		// stamp, so the synthetic value is never displayed.
		if err := db.SetLastSyncTime(time.Now().Add(-autoSyncThreshold + autoSyncRetryAfter)); err != nil {
			return fmt.Errorf("failed to update sync time: %w", err)
		}
	}

	return nil
}

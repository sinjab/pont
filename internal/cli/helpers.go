package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/sinjab/pont/internal/store"
	"github.com/sinjab/pont/internal/whatsapp"
)

// WithDB opens the database and runs the provided function.
// Read commands are PURE local queries: no implicit network connection,
// no surprise socket collisions with the watch daemon, no latency spikes
// (auto-sync removed Oct 11 2026, P1 — refresh explicitly via `sync`;
// connected commands still auto-sync through WithConnection).
func WithDB(fn func(*store.DB) error) error {
	if err := EnsureDirectories(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	db, err := store.Open(GetMessagesDBPath())
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.CloseQuietly()

	return fn(db)
}

// WithConnection opens the database, creates a WhatsApp client, verifies authentication,
// connects to WhatsApp, and runs the provided function.
// Performs auto-sync if last sync was over 24 hours ago.
func WithConnection(fn func(*store.DB, *whatsapp.Client) error) error {
	if err := EnsureDirectories(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	db, err := store.Open(GetMessagesDBPath())
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.CloseQuietly()

	client, err := whatsapp.New(db, GetStoreDir(), IsVerbose(), nil)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	if !client.IsAuthenticated() {
		return fmt.Errorf("not authenticated. Run 'pont auth login' first")
	}

	if err := client.Connect(); err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer client.Disconnect()

	// Wait for the login handshake to complete before running the command.
	// Connect() only opens the websocket; commands that query the server
	// (media download → media-conn) race ahead of login otherwise and the
	// server answers 403 (proven Sep 29 2026 after the protocol update).
	if !client.IsLoggedIn() {
		deadline := time.Now().Add(10 * time.Second)
		for !client.IsLoggedIn() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if !client.IsLoggedIn() {
			fmt.Fprintf(os.Stderr, "warning: login handshake did not complete within 10s; continuing\n")
		}
	}

	// Auto-sync if needed (using existing connection)
	if err := maybeAutoSyncWithClient(client, db); err != nil {
		fmt.Fprintf(os.Stderr, "Auto-sync warning: %v\n", err)
	}

	return fn(db, client)
}

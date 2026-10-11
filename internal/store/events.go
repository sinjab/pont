package store

import (
	"database/sql"
	"time"
)

// WatchEventRow is one durable journal entry from the watch daemon — the
// replay source for consumers that restart (P2: cursor-replay log; the
// stream itself is at-least-once locally, dedup by the consumer via
// id/chat/kind).
type WatchEventRow struct {
	Seq     int64
	TS      time.Time
	Kind    string
	Payload string // the exact JSON line body that was (or will be) emitted
}

// AppendWatchEvent journals one watch event and returns its sequence number.
func (d *DB) AppendWatchEvent(kind string, ts time.Time, payload string) (int64, error) {
	res, err := d.Messages.Exec(
		`INSERT INTO watch_events (ts, kind, payload) VALUES (?, ?, ?)`,
		ts, kind, payload)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// WatchEventsSince returns journaled events with seq > since, ordered.
func (d *DB) WatchEventsSince(since int64) ([]WatchEventRow, error) {
	rows, err := d.Messages.Query(
		`SELECT seq, ts, kind, payload FROM watch_events WHERE seq > ? ORDER BY seq`, since)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			_ = err // best-effort close on a read-only scan path
		}
	}()

	var out []WatchEventRow
	for rows.Next() {
		var r WatchEventRow
		if err := rows.Scan(&r.Seq, &r.TS, &r.Kind, &r.Payload); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestWatchEventSeq returns the newest journal seq (0 when empty).
func (d *DB) LatestWatchEventSeq() (int64, error) {
	var seq sql.NullInt64
	if err := d.Messages.QueryRow(`SELECT MAX(seq) FROM watch_events`).Scan(&seq); err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

// PruneWatchEvents keeps only the newest `keep` journal rows. Called at
// watch startup so the journal stays bounded.
func (d *DB) PruneWatchEvents(keep int64) error {
	_, err := d.Messages.Exec(
		`DELETE FROM watch_events WHERE seq <= (SELECT COALESCE(MAX(seq), 0) FROM watch_events) - ?`, keep)
	return err
}

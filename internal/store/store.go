package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SMS represents a stored SMS message.
type SMS struct {
	ID          string
	Port        int
	Sender      string
	DateTime    string
	Content     string
	Read        string
	ContactName string
	CreatedAt   time.Time
}

// Store is a SQLite-backed storage for SMS messages.
type Store struct {
	db *sql.DB
}

// New creates a new Store, opening (or creating) the SQLite database at dbPath.
// It runs migrations to ensure the schema is up to date.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", dbPath, err)
	}

	// Enable WAL mode for better concurrent read/write performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Set busy timeout so concurrent writes don't immediately fail
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sms (
			id           TEXT    PRIMARY KEY,
			port         INTEGER NOT NULL,
			sender       TEXT    NOT NULL,
			datetime     TEXT    NOT NULL,
			content      TEXT    NOT NULL,
			read         TEXT    NOT NULL DEFAULT 'No',
			contact_name TEXT    NOT NULL DEFAULT '',
			created_at   TEXT    DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		);
		CREATE INDEX IF NOT EXISTS idx_sms_datetime ON sms(datetime DESC);
	`)
	return err
}

// Save inserts an SMS message. Uses INSERT OR IGNORE to silently skip
// duplicates (same ID). Returns true if the record was actually inserted,
// false if it already existed (duplicate).
func (s *Store) Save(sms *SMS) (bool, error) {
	result, err := s.db.Exec(
		`INSERT OR IGNORE INTO sms (id, port, sender, datetime, content, read, contact_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sms.ID, sms.Port, sms.Sender, sms.DateTime, sms.Content, sms.Read, sms.ContactName,
	)
	if err != nil {
		return false, fmt.Errorf("save sms %s: %w", sms.ID, err)
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// GetLastN returns the N most recent SMS messages ordered by datetime descending.
// This is used by the Telegram bot's "last 5" button.
func (s *Store) GetLastN(n int) ([]*SMS, error) {
	if n <= 0 {
		return nil, nil
	}

	rows, err := s.db.Query(
		`SELECT id, port, sender, datetime, content, read, contact_name, created_at
		 FROM sms
		 ORDER BY datetime DESC
		 LIMIT ?`,
		n,
	)
	if err != nil {
		return nil, fmt.Errorf("query last %d: %w", n, err)
	}
	defer rows.Close()

	var messages []*SMS
	for rows.Next() {
		sms := &SMS{}
		var createdAt sql.NullString
		if err := rows.Scan(
			&sms.ID, &sms.Port, &sms.Sender, &sms.DateTime,
			&sms.Content, &sms.Read, &sms.ContactName, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan sms row: %w", err)
		}
		if createdAt.Valid {
			if t, err := time.Parse(time.RFC3339, createdAt.String); err == nil {
				sms.CreatedAt = t
			}
		}
		messages = append(messages, sms)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return messages, nil
}

// Count returns the total number of stored SMS messages.
func (s *Store) Count() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sms").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count sms: %w", err)
	}
	return count, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

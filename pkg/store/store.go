// Package store persists tasks in SQLite or Postgres. The Slack channel is the
// source of truth for what was said; this is the index for owners, status and the board.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nice-pink/itakeit/pkg/task"
	_ "modernc.org/sqlite"
)

// One schema for both databases: BIGINT and BOOLEAN get INTEGER and NUMERIC
// affinity in SQLite, and "user" is quoted because it is reserved in Postgres.
// Databases created before these types existed keep INTEGER columns, which read
// back the same.
const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	channel TEXT NOT NULL, ts TEXT NOT NULL,
	reporter TEXT NOT NULL, text TEXT NOT NULL, permalink TEXT NOT NULL, card_ts TEXT NOT NULL,
	status TEXT NOT NULL, created_at BIGINT NOT NULL, last_activity BIGINT NOT NULL, reminded_at BIGINT NOT NULL,
	PRIMARY KEY (channel, ts));
CREATE TABLE IF NOT EXISTS owners (
	channel TEXT NOT NULL, ts TEXT NOT NULL, pos INTEGER NOT NULL, "user" TEXT NOT NULL,
	PRIMARY KEY (channel, ts, "user"),
	FOREIGN KEY (channel, ts) REFERENCES tasks (channel, ts) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS checks (
	channel TEXT NOT NULL, ts TEXT NOT NULL, item TEXT NOT NULL, checked BOOLEAN NOT NULL,
	PRIMARY KEY (channel, ts, item),
	FOREIGN KEY (channel, ts) REFERENCES tasks (channel, ts) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS reminders (
	channel TEXT NOT NULL, ts TEXT NOT NULL, "user" TEXT NOT NULL, due BIGINT NOT NULL,
	PRIMARY KEY (channel, ts, "user"),
	FOREIGN KEY (channel, ts) REFERENCES tasks (channel, ts) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);`

type Store struct {
	db *sql.DB
	pg bool
}

// Open opens or creates the SQLite file at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return initSchema(&Store{db: db})
}

// OpenPostgres connects with a postgres:// URL or key=value DSN and creates the
// tables if missing. pgx waits forever for a connection by default, and every
// store call runs on the bot's single event goroutine, so a DSN without
// connect_timeout gets 10 s.
// HACK: queries themselves have no timeout; a server that stalls after
// connecting still blocks the bot. Upgrade path: pass a context with a
// deadline through every Store method.
func OpenPostgres(dsn string) (*Store, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx quotes the DSN in its parse error with the password masked only
		// best-effort (key=value DSNs and a stray @ leak it), so drop the quote
		// and keep only the wrapped cause, which does not contain the DSN.
		if cause := errors.Unwrap(err); cause != nil {
			return nil, fmt.Errorf("cannot parse database URL: %w", cause)
		}
		return nil, errors.New("cannot parse database URL")
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	return initSchema(&Store{db: stdlib.OpenDB(*cfg), pg: true})
}

// initSchema runs one statement per Exec so neither driver has to accept a
// multi-statement string.
func initSchema(s *Store) (*Store, error) {
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			s.db.Close()
			return nil, err
		}
	}
	return s, nil
}

// q rewrites ? placeholders to Postgres's $1, $2, ... No query here has a
// literal ? in it, so a plain scan is enough.
func (s *Store) q(query string) string {
	if !s.pg {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) Close() error { return s.db.Close() }

// Get returns nil, nil when the task does not exist.
func (s *Store) Get(channel, ts string) (*task.Task, error) {
	found, err := s.query(`WHERE channel = ? AND ts = ?`, channel, ts)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

// Open lists a channel's tasks that are not done, oldest message first.
func (s *Store) Open(channel string) ([]task.Task, error) {
	return s.query(`WHERE channel = ? AND status != ? ORDER BY CAST(ts AS DOUBLE PRECISION), ts`, channel, string(task.Done))
}

// Save upserts the task and replaces its owner list and checklist ticks.
func (s *Store) Save(t *task.Task) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(s.q(`INSERT INTO tasks VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (channel, ts) DO UPDATE SET reporter=excluded.reporter, text=excluded.text,
		permalink=excluded.permalink, card_ts=excluded.card_ts, status=excluded.status,
		last_activity=excluded.last_activity, reminded_at=excluded.reminded_at`),
		t.Channel, t.TS, t.Reporter, t.Text, t.Permalink, t.CardTS, string(t.Status),
		unix(t.CreatedAt), unix(t.LastActivity), unix(t.RemindedAt))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(s.q(`DELETE FROM owners WHERE channel = ? AND ts = ?`), t.Channel, t.TS); err != nil {
		return err
	}
	for i, o := range t.Owners {
		if _, err := tx.Exec(s.q(`INSERT INTO owners VALUES (?,?,?,?)`), t.Channel, t.TS, i, o); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(s.q(`DELETE FROM checks WHERE channel = ? AND ts = ?`), t.Channel, t.TS); err != nil {
		return err
	}
	for k, c := range t.Checks {
		if _, err := tx.Exec(s.q(`INSERT INTO checks VALUES (?,?,?,?)`), t.Channel, t.TS, k, c); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Delete(channel, ts string) error {
	_, err := s.db.Exec(s.q(`DELETE FROM tasks WHERE channel = ? AND ts = ?`), channel, ts)
	return err
}

// DeleteDone removes a channel's done tasks whose last activity is before cutoff
// and returns how many went. Owners and checks cascade.
func (s *Store) DeleteDone(channel string, cutoff time.Time) (int64, error) {
	r, err := s.db.Exec(s.q(`DELETE FROM tasks WHERE channel = ? AND status = ? AND last_activity < ?`),
		channel, string(task.Done), cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// Reminder is a user's request to be reminded of a task at Due.
type Reminder struct {
	Channel, TS, User string
	Due               time.Time
}

// SetReminder stores the reminder, replacing the user's earlier one on the same task.
func (s *Store) SetReminder(r Reminder) error {
	_, err := s.db.Exec(s.q(`INSERT INTO reminders VALUES (?,?,?,?)
		ON CONFLICT (channel, ts, "user") DO UPDATE SET due = excluded.due`), r.Channel, r.TS, r.User, r.Due.Unix())
	return err
}

// DueReminders lists reminders due at or before now, oldest first.
func (s *Store) DueReminders(now time.Time) ([]Reminder, error) {
	rows, err := s.db.Query(s.q(`SELECT channel, ts, "user", due FROM reminders WHERE due <= ? ORDER BY due, channel, ts`), now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		var r Reminder
		var due int64
		if err := rows.Scan(&r.Channel, &r.TS, &r.User, &due); err != nil {
			return nil, err
		}
		r.Due = time.Unix(due, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteReminder(r Reminder) error {
	_, err := s.db.Exec(s.q(`DELETE FROM reminders WHERE channel = ? AND ts = ? AND "user" = ?`), r.Channel, r.TS, r.User)
	return err
}

// KV returns "" when the key is unset.
func (s *Store) KV(key string) (string, error) {
	var v string
	err := s.db.QueryRow(s.q(`SELECT value FROM kv WHERE key = ?`), key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetKV(key, value string) error {
	_, err := s.db.Exec(s.q(`INSERT INTO kv VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`), key, value)
	return err
}

func (s *Store) query(where string, args ...any) ([]task.Task, error) {
	rows, err := s.db.Query(s.q(`SELECT channel, ts, reporter, text, permalink, card_ts, status,
		created_at, last_activity, reminded_at FROM tasks `+where), args...)
	if err != nil {
		return nil, err
	}
	var out []task.Task
	for rows.Next() {
		var t task.Task
		var status string
		var c, l, r int64
		if err := rows.Scan(&t.Channel, &t.TS, &t.Reporter, &t.Text, &t.Permalink, &t.CardTS, &status, &c, &l, &r); err != nil {
			rows.Close()
			return nil, err
		}
		t.Status, t.CreatedAt, t.LastActivity, t.RemindedAt = task.Action(status), fromUnix(c), fromUnix(l), fromUnix(r)
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Owners, err = s.owners(out[i].Channel, out[i].TS); err != nil {
			return nil, err
		}
		if out[i].Checks, err = s.checks(out[i].Channel, out[i].TS); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) owners(channel, ts string) ([]string, error) {
	rows, err := s.db.Query(s.q(`SELECT "user" FROM owners WHERE channel = ? AND ts = ? ORDER BY pos`), channel, ts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) checks(channel, ts string) (map[string]bool, error) {
	rows, err := s.db.Query(s.q(`SELECT item, checked FROM checks WHERE channel = ? AND ts = ?`), channel, ts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out map[string]bool
	for rows.Next() {
		var k string
		var c bool
		if err := rows.Scan(&k, &c); err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[k] = c
	}
	return out, rows.Err()
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s, 0)
}

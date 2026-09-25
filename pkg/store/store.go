// Package store persists tasks in SQLite. The Slack channel is the source of
// truth for what was said; this is the index for owners, status and the board.
package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/nice-pink/itakeit/pkg/task"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	channel TEXT NOT NULL, ts TEXT NOT NULL,
	reporter TEXT NOT NULL, text TEXT NOT NULL, permalink TEXT NOT NULL, card_ts TEXT NOT NULL,
	status TEXT NOT NULL, created_at INTEGER NOT NULL, last_activity INTEGER NOT NULL, reminded_at INTEGER NOT NULL,
	PRIMARY KEY (channel, ts));
CREATE TABLE IF NOT EXISTS owners (
	channel TEXT NOT NULL, ts TEXT NOT NULL, pos INTEGER NOT NULL, user TEXT NOT NULL,
	PRIMARY KEY (channel, ts, user),
	FOREIGN KEY (channel, ts) REFERENCES tasks (channel, ts) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);`

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
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
	return s.query(`WHERE channel = ? AND status != ? ORDER BY CAST(ts AS REAL), ts`, channel, string(task.Done))
}

// Save upserts the task and replaces its owner list.
func (s *Store) Save(t *task.Task) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO tasks VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (channel, ts) DO UPDATE SET reporter=excluded.reporter, text=excluded.text,
		permalink=excluded.permalink, card_ts=excluded.card_ts, status=excluded.status,
		last_activity=excluded.last_activity, reminded_at=excluded.reminded_at`,
		t.Channel, t.TS, t.Reporter, t.Text, t.Permalink, t.CardTS, string(t.Status),
		unix(t.CreatedAt), unix(t.LastActivity), unix(t.RemindedAt))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM owners WHERE channel = ? AND ts = ?`, t.Channel, t.TS); err != nil {
		return err
	}
	for i, o := range t.Owners {
		if _, err := tx.Exec(`INSERT INTO owners VALUES (?,?,?,?)`, t.Channel, t.TS, i, o); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Delete(channel, ts string) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE channel = ? AND ts = ?`, channel, ts)
	return err
}

// DeleteDone removes a channel's done tasks whose last activity is before cutoff
// and returns how many went. Owners cascade.
func (s *Store) DeleteDone(channel string, cutoff time.Time) (int64, error) {
	r, err := s.db.Exec(`DELETE FROM tasks WHERE channel = ? AND status = ? AND last_activity < ?`,
		channel, string(task.Done), cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// KV returns "" when the key is unset.
func (s *Store) KV(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetKV(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO kv VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) query(where string, args ...any) ([]task.Task, error) {
	rows, err := s.db.Query(`SELECT channel, ts, reporter, text, permalink, card_ts, status,
		created_at, last_activity, reminded_at FROM tasks `+where, args...)
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
	}
	return out, nil
}

func (s *Store) owners(channel, ts string) ([]string, error) {
	rows, err := s.db.Query(`SELECT user FROM owners WHERE channel = ? AND ts = ? ORDER BY pos`, channel, ts)
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

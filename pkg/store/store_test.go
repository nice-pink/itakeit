package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nice-pink/itakeit/pkg/task"
)

func TestRoundTripSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	roundTrip(t, func() (*Store, error) { return Open(path) })
}

// TestRoundTripPostgres runs only when ITAKEIT_TEST_DATABASE_URL points at a
// Postgres database. Each run gets its own schema, dropped afterwards.
func TestRoundTripPostgres(t *testing.T) {
	dsn := os.Getenv("ITAKEIT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ITAKEIT_TEST_DATABASE_URL not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("itakeit_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	roundTrip(t, func() (*Store, error) { return OpenPostgres(dsn + sep + "search_path=" + schema) })
}

func roundTrip(t *testing.T, open func() (*Store, error)) {
	s, err := open()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	in := &task.Task{Channel: "C1", TS: "1.1", Reporter: "UR", Text: "broken", Permalink: "https://x",
		CardTS: "1.2", Status: task.InProgress, Owners: []string{"UB", "UA"}, CreatedAt: now, LastActivity: now,
		Checks: map[string]bool{"a": true, "b": false}}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	in.Owners = []string{"UA"}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	s.Save(&task.Task{Channel: "C1", TS: "2.1", Status: task.Done, CreatedAt: now})
	s.Save(&task.Task{Channel: "C1", TS: "0.5", CreatedAt: now.Add(time.Hour)})
	s.Save(&task.Task{Channel: "C2", TS: "0.1", CreatedAt: now})
	s.Close()

	s, err = open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Get("C1", "1.1")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Text != "broken" || got.Status != task.InProgress || len(got.Owners) != 1 || got.Owners[0] != "UA" ||
		!got.CreatedAt.Equal(now) || !got.RemindedAt.IsZero() || len(got.Checks) != 2 || !got.Checks["a"] || got.Checks["b"] {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	list, _ := s.Open("C1")
	if len(list) != 2 || list[0].TS != "0.5" || list[1].TS != "1.1" {
		t.Fatalf("Open: this channel only, no done tasks, ordered by message ts: %+v", list)
	}
	if n, err := s.DeleteDone("C1", now.Add(time.Second)); n != 1 || err != nil {
		t.Fatalf("DeleteDone: %d %v", n, err)
	}
	if missing, err := s.Get("C1", "9.9"); missing != nil || err != nil {
		t.Fatalf("missing task: %v %v", missing, err)
	}
	if err := s.Delete("C1", "1.1"); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM owners`).Scan(&n)
	if n != 0 {
		t.Fatalf("owners should cascade on delete, %d left", n)
	}
	if v, _ := s.KV("board_ts"); v != "" {
		t.Fatal("unset kv should be empty")
	}
	s.SetKV("board_ts", "5.5")
	s.SetKV("board_ts", "6.6")
	if v, _ := s.KV("board_ts"); v != "6.6" {
		t.Fatalf("kv upsert, got %q", v)
	}
}

func TestReminders(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_700_000_000, 0)
	s.Save(&task.Task{Channel: "C1", TS: "1.1", Status: task.InProgress, CreatedAt: now})
	s.Save(&task.Task{Channel: "C1", TS: "2.1", Status: task.InProgress, CreatedAt: now})
	set := func(ts, user string, due time.Time) {
		if err := s.SetReminder(Reminder{Channel: "C1", TS: ts, User: user, Due: due}); err != nil {
			t.Fatal(err)
		}
	}
	set("1.1", "UA", now.Add(time.Hour))
	set("1.1", "UA", now.Add(3*time.Hour))
	set("1.1", "UB", now.Add(2*time.Hour))
	set("2.1", "UA", now.Add(time.Hour))
	if due, _ := s.DueReminders(now); len(due) != 0 {
		t.Fatalf("nothing is due yet, got %v", due)
	}
	due, err := s.DueReminders(now.Add(150 * time.Minute))
	if err != nil || len(due) != 2 || due[0].TS != "2.1" || due[1].User != "UB" {
		t.Fatalf("replace and order: got %+v %v", due, err)
	}
	s.Delete("C1", "2.1")
	if due, _ = s.DueReminders(now.Add(150 * time.Minute)); len(due) != 1 {
		t.Fatalf("task delete must cascade, got %+v", due)
	}
	if err := s.DeleteReminder(due[0]); err != nil {
		t.Fatal(err)
	}
	if due, _ = s.DueReminders(now.Add(24 * time.Hour)); len(due) != 1 || due[0].User != "UA" {
		t.Fatalf("want only UA's replaced reminder, got %+v", due)
	}
}

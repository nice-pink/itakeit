package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nice-pink/itakeit/pkg/task"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	in := &task.Task{Channel: "C1", TS: "1.1", Reporter: "UR", Text: "broken", Permalink: "https://x",
		CardTS: "1.2", Status: task.InProgress, Owners: []string{"UB", "UA"}, CreatedAt: now, LastActivity: now}
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

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Get("C1", "1.1")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Text != "broken" || got.Status != task.InProgress || len(got.Owners) != 1 || got.Owners[0] != "UA" ||
		!got.CreatedAt.Equal(now) || !got.RemindedAt.IsZero() {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	open, _ := s.Open("C1")
	if len(open) != 2 || open[0].TS != "0.5" || open[1].TS != "1.1" {
		t.Fatalf("Open: this channel only, no done tasks, ordered by message ts: %+v", open)
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

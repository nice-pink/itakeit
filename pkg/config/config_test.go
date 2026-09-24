package config

import (
	"strings"
	"testing"

	"github.com/nice-pink/itakeit/pkg/task"
)

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte("channel: C0123"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != "itakeit.db" || c.StaleAfterHours != 48 || c.BoardMaxTasks != 50 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if a, ok := c.Action("raising_hand::skin-tone-4"); !ok || a != task.Claim {
		t.Fatalf("skin tone variant should resolve to claim, got %q %v", a, ok)
	}
	if _, ok := c.Action("thumbsup"); ok {
		t.Fatal("unmapped emoji must not resolve")
	}
}

func TestCustomEmoji(t *testing.T) {
	c, err := Parse([]byte("channel: C1\nemoji:\n  claim: [\":itakeit:\", hand]\n  done: [heavy_check_mark]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := c.Action("hand"); a != task.Claim {
		t.Fatal("second claim emoji should resolve")
	}
	if c.Display()[task.Claim] != "itakeit" {
		t.Fatalf("first listed emoji is displayed, colons trimmed: %v", c.Display())
	}
	if _, ok := c.Action("eyes"); ok {
		t.Fatal("a custom emoji map replaces the defaults entirely")
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string]string{
		"missing channel": "db_path: x.db",
		"unknown action":  "channel: C1\nemoji:\n  claim: [a]\n  yolo: [b]",
		"no claim":        "channel: C1\nemoji:\n  done: [a]",
		"duplicate emoji": "channel: C1\nemoji:\n  claim: [a]\n  done: [a]",
		"bad yaml":        "channel: [",
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if testing.Verbose() {
			t.Logf("%s: %v", name, strings.TrimSpace(err.Error()))
		}
	}
}

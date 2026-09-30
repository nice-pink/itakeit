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
	if c.DBPath != "itakeit.db" || c.DatabaseURL != "" || c.StaleAfterHours != 48 || c.BoardMaxTasks != 50 || c.DoneRetainDays != 30 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if a, ok := c.Action("raising_hand::skin-tone-4"); !ok || a != task.Claim {
		t.Fatalf("skin tone variant should resolve to claim, got %q %v", a, ok)
	}
	if _, ok := c.Action("thumbsup"); ok {
		t.Fatal("unmapped emoji must not resolve")
	}
}

func TestChannels(t *testing.T) {
	c, err := Parse([]byte("channels: [C1, C2]"))
	if err != nil || !c.Serves("C1") || !c.Serves("C2") || c.Serves("C3") {
		t.Fatalf("channels not read: %+v %v", c, err)
	}
	if c, err := Parse([]byte("channels: [\" C1 \"]")); err != nil || !c.Serves("C1") {
		t.Fatalf("channel IDs are trimmed: %+v %v", c, err)
	}
	c, err = Parse([]byte("channel: C1"))
	if err != nil || len(c.Channels) != 1 || !c.Serves("C1") {
		t.Fatalf("the single channel key must still work: %+v %v", c, err)
	}
}

func TestAutoChannels(t *testing.T) {
	c, err := Parse([]byte("auto_channels: true"))
	if err != nil || !c.AutoChannels || len(c.Channels) != 0 || c.Serves("C1") {
		t.Fatalf("auto_channels needs no list and serves nothing by itself: %+v %v", c, err)
	}
	// An older itakeit-agent sharing the file needs channel, so auto mode keeps and ignores it.
	c, err = Parse([]byte("auto_channels: true\nchannel: C1"))
	if err != nil || len(c.Channels) != 0 || c.Serves("C1") || c.LegacyChannel != "C1" {
		t.Fatalf("channel must be kept but ignored under auto_channels: %+v %v", c, err)
	}
	if !IsChannelID("C0123") || !IsChannelID("G0123") || IsChannelID("D0123") || IsChannelID("#itakeit") {
		t.Fatal("IsChannelID must accept public and private channel IDs only")
	}
}

func TestDatabaseURL(t *testing.T) {
	c, err := Parse([]byte("channel: C1\ndatabase_url: postgres://u:p@h/db"))
	if err != nil || c.DatabaseURL != "postgres://u:p@h/db" {
		t.Fatalf("database_url not read: %+v %v", c, err)
	}
}

func TestDoneRetainOff(t *testing.T) {
	c, err := Parse([]byte("channel: C1\ndone_retain_days: -1"))
	if err != nil || c.DoneRetain() != 0 {
		t.Fatalf("-1 must disable cleanup, got %v %v", c, err)
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
		"empty channels":  "channels: []",
		"both keys":       "channel: C1\nchannels: [C2]",
		"auto and list":   "auto_channels: true\nchannels: [C1]",
		"twice":           "channels: [C1, C1]",
		"blank channel":   "channels: [C1, \"\"]",
		"channel name":    "channels: [\"#itakeit\"]",
		"unknown action":  "channel: C1\nemoji:\n  claim: [a]\n  yolo: [b]",
		"no claim":        "channel: C1\nemoji:\n  done: [a]",
		"duplicate emoji": "channel: C1\nemoji:\n  claim: [a]\n  done: [a]",
		"bad yaml":        "channel: [",
		"negative retain": "channel: C1\ndone_retain_days: -2",
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if testing.Verbose() {
			t.Logf("%s: %v", name, strings.TrimSpace(err.Error()))
		}
	}
}

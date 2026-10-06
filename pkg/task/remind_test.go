package task

import (
	"testing"
	"time"
)

func TestParseRemind(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	now := time.Date(2026, 10, 7, 10, 30, 0, 0, loc) // Wednesday
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	tests := []struct {
		in   string
		want time.Time
	}{
		{"remind me tomorrow", at(8, 9, 0)},
		{"Remind me tomorrow.", at(8, 9, 0)},
		{"remind me tomorrow at 3pm", at(8, 15, 0)},
		{"remind me tomorrow 15:30", at(8, 15, 30)},
		{"remind me in 3h", at(7, 13, 30)},
		{"remind me in 2 days", at(9, 10, 30)},
		{"remind me in 30 minutes", at(7, 11, 0)},
		{"remind me in 1w", at(14, 10, 30)},
		{"remind me wednesday", at(14, 9, 0)},
		{"remind me friday at 12am", at(9, 0, 0)},
		{"remind me at 15:00", at(7, 15, 0)},
		{"remind me at 9:00", at(8, 9, 0)},
		{"remind me today at 18:00", at(7, 18, 0)},
		{"remind me   monday   at 8am", at(12, 8, 0)},
		{"remind me\ntomorrow", at(8, 9, 0)},
	}
	for _, tc := range tests {
		got, ok := ParseRemind(tc.in, now, loc)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("%q = %v %v, want %v", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []string{"remind me", "remind me what the repro was", "remind me today", "remind me today at 9:00",
		"remind me monday 3", "remind me at 25:00", "remind me in 0h", "remind me at 13pm", "please remind me tomorrow",
		"remind me in 5 years", "remind me tomorrow at 10:75", "remindme tomorrow", "remind men tomorrow", "remind me at 3", "remind me at 15", "remind me tomorrowat 3pm"} {
		if got, ok := ParseRemind(in, now, loc); ok {
			t.Errorf("%q parsed as %v", in, got)
		}
	}
}

func TestParseRemindDST(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, loc) // clocks go back on 25 Oct
	got, ok := ParseRemind("remind me tomorrow", now, loc)
	if want := time.Date(2026, 10, 25, 9, 0, 0, 0, loc); !ok || !got.Equal(want) {
		t.Errorf("got %v %v, want %v", got, ok, want)
	}
}

func TestParseRemindCalendarDays(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	now := time.Date(2026, 3, 28, 20, 0, 0, 0, loc) // clocks go forward on 29 Mar
	got, ok := ParseRemind("remind me in 1d", now, loc)
	if want := time.Date(2026, 3, 29, 20, 0, 0, 0, loc); !ok || !got.Equal(want) {
		t.Errorf("got %v %v, want %v", got, ok, want)
	}
}

func TestIsRemind(t *testing.T) {
	for in, want := range map[string]bool{
		"remind me": true, "remind me.": true, "Remind me: tomorrow": true, "Remind me tomorrow": true, " remind me\tat 9:00": true, "remind me what the repro was": true,
		"remindme": false, "remind men": false, "please remind me": false,
	} {
		if got := IsRemind(in); got != want {
			t.Errorf("IsRemind(%q) = %v, want %v", in, got, want)
		}
	}
}

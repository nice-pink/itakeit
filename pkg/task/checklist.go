package task

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Item is one checklist line of the task message. Key is stable across edits
// that keep the line's text, so a tick survives reordering.
type Item struct {
	Key     string
	Text    string
	Checked bool
}

// A checklist line is "[ ] text" or "[x] text", optionally behind a list bullet.
var checkLine = regexp.MustCompile(`^(?:[•◦▪*-]\s*)?\[([ xX])\]\s+(\S.*)$`)

// Checklist parses the explicit checklist lines of the task text. A tick set on
// the card overrides the [ ]/[x] written in the message.
func (t Task) Checklist() []Item {
	var out []Item
	seen := map[string]int{}
	for _, line := range strings.Split(t.Text, "\n") {
		m := checkLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[2])
		seen[text]++
		sum := sha1.Sum(fmt.Appendf(nil, "%s\x00%d", text, seen[text]))
		it := Item{Key: hex.EncodeToString(sum[:5]), Text: text, Checked: m[1] != " "}
		if c, ok := t.Checks[it.Key]; ok {
			it.Checked = c
		}
		out = append(out, it)
	}
	return out
}

// SetText replaces the task text and drops ticks of items it no longer has.
func (t *Task) SetText(text string) {
	t.Text = text
	keep := map[string]bool{}
	for _, it := range t.Checklist() {
		keep[it.Key] = true
	}
	for k := range t.Checks {
		if !keep[k] {
			delete(t.Checks, k)
		}
	}
}

// Progress counts ticked and total checklist items.
func (t Task) Progress() (done, total int) {
	for _, it := range t.Checklist() {
		total++
		if it.Checked {
			done++
		}
	}
	return done, total
}

// Check applies ticks from the card. Anyone may tick; keys no longer in the
// message are ignored. An owner's tick counts as activity. Completing the
// checklist on an open task returns ChecklistDone.
func (t *Task) Check(user string, set map[string]bool, now time.Time) Effect {
	before, total := t.Progress()
	changed := false
	for _, it := range t.Checklist() {
		if c, ok := set[it.Key]; ok && c != it.Checked {
			if t.Checks == nil {
				t.Checks = map[string]bool{}
			}
			t.Checks[it.Key] = c
			changed = true
		}
	}
	if !changed {
		return NoChange
	}
	if t.IsOwner(user) {
		t.LastActivity = now
		t.RemindedAt = time.Time{}
	}
	if after, _ := t.Progress(); after == total && before < total && t.Open() {
		return ChecklistDone
	}
	return Changed
}

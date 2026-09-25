// Package task holds the task model and its state transitions. No I/O.
package task

import (
	"slices"
	"time"
)

type Action string

const (
	Claim         Action = "claim"
	Investigating Action = "investigating"
	InProgress    Action = "in_progress"
	NeedsInfo     Action = "needs_info"
	Blocked       Action = "blocked"
	Done          Action = "done"
)

var statusActions = []Action{Investigating, InProgress, NeedsInfo, Blocked, Done}

func ValidAction(a Action) bool { return a == Claim || slices.Contains(statusActions, a) }

// Task is one top-level message in the channel. Status is empty until an owner
// sets one explicitly; Label derives "unclaimed"/"claimed" from the owners.
type Task struct {
	Channel      string
	TS           string
	Reporter     string // Slack user ID, or a bot name when posted by an integration
	Text         string
	Permalink    string
	CardTS       string // the bot's status reply in the task thread
	Status       Action
	Owners       []string
	CreatedAt    time.Time
	LastActivity time.Time // last claim, status change or thread reply by an owner
	RemindedAt   time.Time
	Checks       map[string]bool // checklist ticks set on the card, by Item.Key
}

type Effect int

const (
	NoChange     Effect = iota
	Changed             // state changed: re-render card and board
	Denied              // a non-owner tried to set a status
	AskReporter         // changed to needs_info: ping the reporter
	NotifyOwners        // reporter answered a needs_info: ping the owners
	ChecklistDone       // last checklist item ticked: nudge to close
)

func (t *Task) IsOwner(user string) bool { return slices.Contains(t.Owners, user) }

func (t *Task) Open() bool { return t.Status != Done }

// React applies a reaction added to or removed from the task message.
func (t *Task) React(a Action, user string, added bool, now time.Time) Effect {
	if a == Claim {
		return t.claim(user, added, now)
	}
	if !t.IsOwner(user) {
		if added {
			return Denied
		}
		return NoChange
	}
	if !added {
		if t.Status != a {
			return NoChange
		}
		t.Status = ""
		t.LastActivity = now
		return Changed
	}
	t.Status = a
	t.LastActivity = now
	if a == NeedsInfo {
		return AskReporter
	}
	return Changed
}

// ReactOpen is React for status_claims mode, where a status reaction needs no
// claim first: any claim or status reaction makes the reactor an owner, and they
// stay one while they hold at least one. holding reports, for a removal, whether
// the user still has another claim or status reaction on the message.
func (t *Task) ReactOpen(a Action, user string, added, holding bool, now time.Time) Effect {
	eff := NoChange
	if added && !t.IsOwner(user) {
		t.claim(user, true, now)
		eff = Changed
	}
	if a != Claim {
		if e := t.React(a, user, added, now); e != NoChange {
			eff = e
		}
	}
	if !added && !holding && t.IsOwner(user) {
		t.claim(user, false, now)
		if eff == NoChange {
			eff = Changed
		}
	}
	return eff
}

func (t *Task) claim(user string, added bool, now time.Time) Effect {
	switch {
	case added && !t.IsOwner(user):
		t.Owners = append(t.Owners, user)
	case !added && t.IsOwner(user):
		t.Owners = slices.DeleteFunc(t.Owners, func(o string) bool { return o == user })
		if len(t.Owners) == 0 && t.Status != Done {
			t.Status = ""
		}
	default:
		return NoChange
	}
	t.LastActivity = now
	t.RemindedAt = time.Time{}
	return Changed
}

// Reply applies a thread reply. Owner replies count as activity. A reporter reply
// while the task waits on them clears needs_info.
func (t *Task) Reply(user string, now time.Time) Effect {
	if t.IsOwner(user) {
		t.LastActivity = now
		t.RemindedAt = time.Time{}
	}
	if user == t.Reporter && t.Status == NeedsInfo {
		t.Status = ""
		t.LastActivity = now
		return NotifyOwners
	}
	return NoChange
}

// Stale reports whether owners should be nudged: claimed, actively being worked,
// and silent for longer than after, with no reminder inside that window either.
func (t *Task) Stale(now time.Time, after time.Duration) bool {
	if after <= 0 || len(t.Owners) == 0 {
		return false
	}
	if t.Status != "" && t.Status != Investigating && t.Status != InProgress {
		return false
	}
	return now.Sub(t.LastActivity) >= after && now.Sub(t.RemindedAt) >= after
}

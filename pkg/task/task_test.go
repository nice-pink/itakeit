package task

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

func TestClaimAndRelease(t *testing.T) {
	tk := &Task{Reporter: "UREPORT01"}
	if e := tk.React(Claim, "UALICE001", true, t0); e != Changed || !tk.IsOwner("UALICE001") {
		t.Fatalf("claim: effect %v owners %v", e, tk.Owners)
	}
	if e := tk.React(Claim, "UALICE001", true, t0); e != NoChange {
		t.Fatalf("double claim should be a no-op, got %v", e)
	}
	tk.React(Claim, "UBOB00001", true, t0)
	if strings.Join(tk.Owners, ",") != "UALICE001,UBOB00001" {
		t.Fatalf("owners keep claim order, got %v", tk.Owners)
	}
	tk.React(InProgress, "UALICE001", true, t0)
	tk.React(Claim, "UALICE001", false, t0)
	if tk.Status != InProgress {
		t.Fatalf("status survives while an owner remains, got %q", tk.Status)
	}
	tk.React(Claim, "UBOB00001", false, t0)
	if len(tk.Owners) != 0 || tk.Status != "" {
		t.Fatalf("last release resets status, got owners %v status %q", tk.Owners, tk.Status)
	}
}

func TestStatusOnlyFromOwners(t *testing.T) {
	tk := &Task{Reporter: "UREPORT01"}
	if e := tk.React(Done, "UMALLORY1", true, t0); e != Denied || tk.Status != "" {
		t.Fatalf("non-owner status: effect %v status %q", e, tk.Status)
	}
	if e := tk.React(Done, "UMALLORY1", false, t0); e != NoChange {
		t.Fatalf("non-owner removal should be silent, got %v", e)
	}
	tk.React(Claim, "UALICE001", true, t0)
	if e := tk.React(Blocked, "UALICE001", true, t0); e != Changed || tk.Status != Blocked {
		t.Fatalf("owner status: effect %v status %q", e, tk.Status)
	}
	if e := tk.React(InProgress, "UALICE001", false, t0); e != NoChange || tk.Status != Blocked {
		t.Fatalf("removing a non-current status is a no-op, got %v %q", e, tk.Status)
	}
	if e := tk.React(Blocked, "UALICE001", false, t0); e != Changed || tk.Status != "" {
		t.Fatalf("removing the current status clears it, got %v %q", e, tk.Status)
	}
}

func TestNeedsInfoRoundTrip(t *testing.T) {
	tk := &Task{Reporter: "UREPORT01"}
	tk.React(Claim, "UALICE001", true, t0)
	if e := tk.React(NeedsInfo, "UALICE001", true, t0); e != AskReporter {
		t.Fatalf("needs_info should ask the reporter, got %v", e)
	}
	if e := tk.Reply("USOMEONE1", t0); e != NoChange || tk.Status != NeedsInfo {
		t.Fatalf("a bystander reply does not answer, got %v %q", e, tk.Status)
	}
	if e := tk.Reply("UREPORT01", t0); e != NotifyOwners || tk.Status != "" {
		t.Fatalf("reporter reply clears needs_info, got %v %q", e, tk.Status)
	}
}

func TestStale(t *testing.T) {
	day := 24 * time.Hour
	tk := &Task{Owners: []string{"UALICE001"}, LastActivity: t0}
	if tk.Stale(t0.Add(day-time.Minute), day) {
		t.Fatal("not stale before the window")
	}
	if !tk.Stale(t0.Add(day), day) {
		t.Fatal("stale after the window")
	}
	tk.RemindedAt = t0.Add(day)
	if tk.Stale(t0.Add(day+time.Hour), day) {
		t.Fatal("no second reminder inside the window")
	}
	tk.Status = Blocked
	if tk.Stale(t0.Add(10*day), day) {
		t.Fatal("blocked tasks are waiting on someone else, no nudge")
	}
	if (&Task{LastActivity: t0}).Stale(t0.Add(10*day), day) {
		t.Fatal("unclaimed tasks are not nudged")
	}
	tk.Status = ""
	tk.Reply("UALICE001", t0.Add(10*day))
	if tk.Stale(t0.Add(10*day+time.Hour), day) {
		t.Fatal("owner reply resets the clock")
	}
}

func TestExcerpt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"short", "short"},
		{"first line\nsecond", "first line"},
		{"abcdefgh <@U123456789> tail", "abcdefgh…"},
		{"abcdefgh &amp; more text", "abcdefgh…"},
		{"abcdefghij klm", "abcdefghij…"},
	}
	for _, c := range cases {
		if got := Excerpt(c.in, 10); got != c.want {
			t.Errorf("Excerpt(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMention(t *testing.T) {
	if Mention("U0123ABCD") != "<@U0123ABCD>" || Mention("Uptime Robot") != "Uptime Robot" {
		t.Fatal("only real user IDs become mentions")
	}
}

func TestBoard(t *testing.T) {
	emoji := map[Action]string{Claim: "raising_hand", InProgress: "construction"}
	tasks := []Task{
		{Text: "db down", Permalink: "https://x/p1", Owners: []string{"UALICE001"}, Status: InProgress},
		{Text: "typo on pricing page", Permalink: "https://x/p2"},
	}
	b := Board(tasks, emoji, 1, t0)
	for _, want := range []string{"2 open", ":white_circle: 1 unclaimed · :construction: 1 in progress\n", ":construction: in progress", "<@UALICE001>", "<https://x/p1|open>", "…and 1 more"} {
		if !strings.Contains(b, want) {
			t.Errorf("board missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "typo") {
		t.Errorf("board should stop at max:\n%s", b)
	}
	mixed := append(tasks, Task{Owners: []string{"UBOB00001"}}, Task{Owners: []string{"UBOB00001"}, Status: Blocked}, Task{})
	if want := ":white_circle: 2 unclaimed · :raising_hand: 1 claimed · :construction: 1 in progress · 1 blocked\n"; !strings.Contains(Board(mixed, emoji, 1, t0), want) {
		t.Errorf("counts cover every task in state order, icon-less when the emoji is disabled:\n%s", Board(mixed, emoji, 1, t0))
	}
	if !strings.Contains(Board(nil, emoji, 10, t0), "Nothing open") {
		t.Error("empty board")
	}
}

func TestChecklistParse(t *testing.T) {
	tk := Task{Text: "title\n[ ] a\n• [x] b\n- [X] a\nnot [ ] an item\n[ ]nospace\n* [ ]   c  "}
	got := tk.Checklist()
	if len(got) != 4 {
		t.Fatalf("want 4 items, got %+v", got)
	}
	want := []Item{{Text: "a"}, {Text: "b", Checked: true}, {Text: "a", Checked: true}, {Text: "c"}}
	for i, w := range want {
		if got[i].Text != w.Text || got[i].Checked != w.Checked {
			t.Fatalf("item %d: want %+v got %+v", i, w, got[i])
		}
	}
	if got[0].Key == got[2].Key {
		t.Fatal("duplicate item texts need distinct keys")
	}
}

func TestCheck(t *testing.T) {
	tk := &Task{Text: "[ ] a\n[x] b", Owners: []string{"UALICE001"}}
	a, b := tk.Checklist()[0].Key, tk.Checklist()[1].Key
	if e := tk.Check("UBYSTAND1", map[string]bool{b: true, "gone": true}, t0); e != NoChange {
		t.Fatalf("no change expected, got %v", e)
	}
	if e := tk.Check("UBYSTAND1", map[string]bool{b: false}, t0); e != Changed || tk.Checklist()[1].Checked {
		t.Fatalf("a card untick overrides [x], got %v", e)
	}
	if !tk.LastActivity.IsZero() {
		t.Fatal("a non-owner tick is not owner activity")
	}
	if e := tk.Check("UALICE001", map[string]bool{a: true, b: true}, t0); e != ChecklistDone || !tk.LastActivity.Equal(t0) {
		t.Fatalf("completing should report ChecklistDone and count as activity, got %v", e)
	}
	tk.Checks, tk.Status = nil, Done
	if e := tk.Check("UALICE001", map[string]bool{a: true}, t0); e != Changed {
		t.Fatalf("no nudge on a done task, got %v", e)
	}
}

func TestSetTextPrunesTicks(t *testing.T) {
	tk := &Task{Text: "[ ] a\n[ ] b"}
	a, b := tk.Checklist()[0].Key, tk.Checklist()[1].Key
	tk.Check("U1", map[string]bool{a: true, b: true}, t0)
	tk.SetText("[ ] b")
	if len(tk.Checks) != 1 || !tk.Checks[b] {
		t.Fatalf("removed item's tick should go, kept one should stay: %v", tk.Checks)
	}
}

func TestReactOpen(t *testing.T) {
	tk := &Task{Reporter: "UREPORT01"}
	if e := tk.ReactOpen(Blocked, "UALICE001", true, false, t0); e != Changed || !tk.IsOwner("UALICE001") || tk.Status != Blocked {
		t.Fatalf("status without claim: effect %v owners %v status %q", e, tk.Owners, tk.Status)
	}
	if e := tk.ReactOpen(NeedsInfo, "UBOB00001", true, false, t0); e != AskReporter || !tk.IsOwner("UBOB00001") {
		t.Fatalf("needs_info keeps its effect when it also claims, got %v", e)
	}
	if e := tk.ReactOpen(Claim, "UBOB00001", true, false, t0); e != NoChange {
		t.Fatalf("claiming when already an owner is a no-op, got %v", e)
	}
	if e := tk.ReactOpen(Claim, "UBOB00001", false, true, t0); e != NoChange || !tk.IsOwner("UBOB00001") {
		t.Fatalf("an owner still holding a status stays, got %v %v", e, tk.Owners)
	}
	if e := tk.ReactOpen(NeedsInfo, "UBOB00001", false, false, t0); e != Changed || tk.IsOwner("UBOB00001") || tk.Status != "" {
		t.Fatalf("last reaction removed: effect %v owners %v status %q", e, tk.Owners, tk.Status)
	}
	if e := tk.ReactOpen(Blocked, "UALICE001", false, false, t0); e != Changed || len(tk.Owners) != 0 || tk.Status != "" {
		t.Fatalf("last owner gone resets: effect %v owners %v status %q", e, tk.Owners, tk.Status)
	}
	if e := tk.ReactOpen(Done, "UMALLORY1", false, false, t0); e != NoChange {
		t.Fatalf("removal by a non-owner is silent, got %v", e)
	}
}

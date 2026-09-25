package bot

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nice-pink/itakeit/pkg/config"
	"github.com/nice-pink/itakeit/pkg/store"
	"github.com/nice-pink/itakeit/pkg/task"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

const ch = "CTASKS001"

type call struct{ kind, channel, ts, thread, user, text string }

// fakeAPI records every Slack call and hands out sequential timestamps.
type fakeAPI struct {
	calls   []call
	seq     int
	history map[string]slack.Message
	missing map[string]bool // ts that UpdateMessage reports as message_not_found
}

func decode(opts []slack.MsgOption) (text, thread string) {
	_, v, _ := slack.UnsafeApplyMsgOptions("", "", "", opts...)
	return v.Get("text"), v.Get("thread_ts")
}

func (f *fakeAPI) next() string { f.seq++; return fmt.Sprintf("900.%d", f.seq) }

func (f *fakeAPI) PostMessage(c string, o ...slack.MsgOption) (string, string, error) {
	text, thread := decode(o)
	ts := f.next()
	f.calls = append(f.calls, call{kind: "post", channel: c, ts: ts, thread: thread, text: text})
	return c, ts, nil
}

func (f *fakeAPI) UpdateMessage(c, ts string, o ...slack.MsgOption) (string, string, string, error) {
	if f.missing[ts] {
		return "", "", "", slack.SlackErrorResponse{Err: "message_not_found"}
	}
	text, _ := decode(o)
	f.calls = append(f.calls, call{kind: "update", channel: c, ts: ts, text: text})
	return c, ts, text, nil
}

func (f *fakeAPI) DeleteMessage(c, ts string) (string, string, error) {
	f.calls = append(f.calls, call{kind: "delete", channel: c, ts: ts})
	return c, ts, nil
}

func (f *fakeAPI) PostEphemeral(c, u string, o ...slack.MsgOption) (string, error) {
	text, thread := decode(o)
	f.calls = append(f.calls, call{kind: "ephemeral", channel: c, user: u, thread: thread, text: text})
	return "", nil
}

func (f *fakeAPI) GetPermalink(p *slack.PermalinkParameters) (string, error) {
	return "https://example.slack.com/archives/" + p.Channel + "/p" + strings.ReplaceAll(p.Ts, ".", ""), nil
}

func (f *fakeAPI) GetConversationHistory(p *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	r := &slack.GetConversationHistoryResponse{}
	if m, ok := f.history[p.Latest]; ok {
		r.Messages = []slack.Message{m}
	}
	return r, nil
}

func (f *fakeAPI) AddPin(c string, item slack.ItemRef) error {
	f.calls = append(f.calls, call{kind: "pin", channel: c, ts: item.Timestamp})
	return nil
}

func (f *fakeAPI) reset() { f.calls = nil }

func (f *fakeAPI) find(kind, contains string) *call {
	for i := range f.calls {
		if f.calls[i].kind == kind && strings.Contains(f.calls[i].text, contains) {
			return &f.calls[i]
		}
	}
	return nil
}

func setup(t *testing.T) (*Bot, *fakeAPI, *store.Store) {
	t.Helper()
	cfg, err := config.Parse([]byte("channel: " + ch + "\nstale_after_hours: 24\ndone_retain_days: -1"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fakeAPI{history: map[string]slack.Message{}, missing: map[string]bool{}}
	b := New(f, st, cfg, "UBOT00001", "BBOT00001")
	now := time.Unix(1_700_000_000, 0)
	b.now = func() time.Time { return now }
	return b, f, st
}

func msg(ev *slackevents.MessageEvent) slackevents.EventsAPIEvent {
	ev.Channel = ch
	return slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{Data: ev}}
}

func react(user, emoji, ts string, added bool) slackevents.EventsAPIEvent {
	item := slackevents.Item{Type: "message", Channel: ch, Timestamp: ts}
	var data any = &slackevents.ReactionAddedEvent{User: user, Reaction: emoji, Item: item}
	if !added {
		data = &slackevents.ReactionRemovedEvent{User: user, Reaction: emoji, Item: item}
	}
	return slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{Data: data}}
}

func TestLifecycle(t *testing.T) {
	b, f, st := setup(t)

	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "checkout returns 500"}))
	card := f.find("post", "unclaimed")
	if card == nil || card.thread != "100.1" {
		t.Fatalf("expected a status card in the task thread, calls: %+v", f.calls)
	}
	board := f.find("post", "loading board")
	if board == nil || board.thread != "" || f.find("pin", "") == nil {
		t.Fatalf("expected a pinned top-level board, calls: %+v", f.calls)
	}
	if f.find("post", "1 open") != nil || f.find("update", "1 open") == nil {
		t.Fatalf("board content must arrive by edit so its mentions never notify, calls: %+v", f.calls)
	}

	// The bot's own card and board echo back as message events and must be ignored.
	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{User: "UBOT00001", BotID: "BBOT00001", TimeStamp: board.ts, Text: "board"}))
	b.Handle(msg(&slackevents.MessageEvent{SubType: "pinned_item", User: "UBOT00001", TimeStamp: "100.9"}))
	if len(f.calls) != 0 {
		t.Fatalf("own messages must not create tasks: %+v", f.calls)
	}

	b.Handle(react("UALICE001", "raising_hand::skin-tone-2", "100.1", true))
	if c := f.find("update", "<@UALICE001>"); c == nil || c.ts != card.ts {
		t.Fatalf("claim should update the card, calls: %+v", f.calls)
	}

	f.reset()
	b.Handle(react("UMALLORY1", "white_check_mark", "100.1", true))
	if c := f.find("ephemeral", "Only owners"); c == nil || c.user != "UMALLORY1" {
		t.Fatalf("non-owner status should be refused privately, calls: %+v", f.calls)
	}

	f.reset()
	b.Handle(react("UALICE001", "question", "100.1", true))
	if c := f.find("post", "<@UREPORT01>: <@UALICE001> needs more details"); c == nil || c.thread != "100.1" {
		t.Fatalf("needs_info should ping the reporter in the thread, calls: %+v", f.calls)
	}

	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.5", ThreadTimeStamp: "100.1", Text: "it's the EU region"}))
	if f.find("post", "<@UALICE001>: <@UREPORT01> added details") == nil {
		t.Fatalf("reporter reply should notify owners, calls: %+v", f.calls)
	}

	f.reset()
	b.Handle(react("UALICE001", "white_check_mark", "100.1", true))
	if f.find("update", "Nothing open") == nil {
		t.Fatalf("done task should leave the board, calls: %+v", f.calls)
	}
	tk, _ := st.Get(ch, "100.1")
	if tk.Status != task.Done {
		t.Fatalf("status %q", tk.Status)
	}
}

func TestAdoptEditDelete(t *testing.T) {
	b, f, st := setup(t)
	f.history["50.1"] = slack.Message{Msg: slack.Msg{User: "UOLD00001", Timestamp: "50.1", Text: "old issue"}}

	b.Handle(react("UALICE001", "raising_hand", "50.1", true))
	tk, _ := st.Get(ch, "50.1")
	if tk == nil || tk.Reporter != "UOLD00001" || !tk.IsOwner("UALICE001") {
		t.Fatalf("reaction on an untracked message should adopt it, got %+v", tk)
	}

	b.Handle(react("UALICE001", "raising_hand", "77.7", true))
	if tk, _ := st.Get(ch, "77.7"); tk != nil {
		t.Fatal("a reaction on something missing from history (a thread reply) must not create a task")
	}

	b.Handle(msg(&slackevents.MessageEvent{SubType: "message_changed", Message: &slack.Msg{Timestamp: "50.1", Text: "old issue, now worse"}}))
	if tk, _ := st.Get(ch, "50.1"); tk.Text != "old issue, now worse" {
		t.Fatalf("edit not applied: %q", tk.Text)
	}

	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{SubType: "message_deleted", DeletedTimeStamp: "50.1"}))
	if tk, _ := st.Get(ch, "50.1"); tk != nil || f.find("delete", "") == nil {
		t.Fatalf("delete should drop the task and its card, calls: %+v", f.calls)
	}
}

func TestBoardRepostedWhenDeleted(t *testing.T) {
	b, f, st := setup(t)
	b.refreshBoard()
	old, _ := st.KV(boardKey(ch))
	f.missing[old] = true
	f.reset()
	b.refreshBoard()
	now, _ := st.KV(boardKey(ch))
	if now == old || f.find("pin", "") == nil {
		t.Fatalf("a deleted board should be reposted and pinned, calls: %+v", f.calls)
	}
}

func TestRemindStale(t *testing.T) {
	b, f, st := setup(t)
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "flaky test"}))
	b.Handle(react("UALICE001", "raising_hand", "100.1", true))
	start := b.now()

	f.reset()
	b.now = func() time.Time { return start.Add(23 * time.Hour) }
	b.remindStale()
	if len(f.calls) != 0 {
		t.Fatal("no reminder inside the window")
	}
	b.now = func() time.Time { return start.Add(25 * time.Hour) }
	b.remindStale()
	b.remindStale()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c.text, "Still on it?") {
			n++
		}
	}
	if n != 1 || f.find("post", "no update here for 25 hours") == nil {
		t.Fatalf("expected exactly one reminder per window, got %d: %+v", n, f.calls)
	}
	if tk, _ := st.Get(ch, "100.1"); tk.RemindedAt.IsZero() {
		t.Fatal("reminder time not persisted")
	}
}

func TestRedeliveryKeepsState(t *testing.T) {
	b, f, st := setup(t)
	ev := &slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "outage"}
	b.Handle(msg(ev))
	b.Handle(react("UALICE001", "raising_hand", "100.1", true))
	b.Handle(react("UALICE001", "construction", "100.1", true))

	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "outage"}))
	tk, _ := st.Get(ch, "100.1")
	if !tk.IsOwner("UALICE001") || tk.Status != task.InProgress || len(f.calls) != 0 {
		t.Fatalf("a redelivered message must be a no-op, got %+v, calls %+v", tk, f.calls)
	}
}

func TestTombstoneDeletesTask(t *testing.T) {
	b, f, st := setup(t)
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "outage"}))
	card, _ := st.Get(ch, "100.1")

	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{SubType: "message_changed",
		Message: &slack.Msg{SubType: "tombstone", Timestamp: "100.1", Text: "This message was deleted."}}))
	if tk, _ := st.Get(ch, "100.1"); tk != nil {
		t.Fatalf("tombstoned task should be removed, got %+v", tk)
	}
	if c := f.find("delete", ""); c == nil || c.ts != card.CardTS || f.find("update", "Nothing open") == nil {
		t.Fatalf("card deleted and board emptied expected, calls: %+v", f.calls)
	}
}

func TestSweepDone(t *testing.T) {
	b, f, st := setup(t)
	b.cfg.DoneRetainDays = 7
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "old"}))
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.2", Text: "open"}))
	b.Handle(react("UALICE001", "raising_hand", "100.1", true))
	b.Handle(react("UALICE001", "white_check_mark", "100.1", true))
	start := b.now()

	b.now = func() time.Time { return start.Add(6 * 24 * time.Hour) }
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.3", Text: "recent"}))
	b.Handle(react("UALICE001", "raising_hand", "100.3", true))
	b.Handle(react("UALICE001", "white_check_mark", "100.3", true))
	b.sweepDone()
	if tk, _ := st.Get(ch, "100.1"); tk == nil {
		t.Fatal("done task inside the retention window was swept")
	}

	b.now = func() time.Time { return start.Add(8 * 24 * time.Hour) }
	b.sweepDone()
	if tk, _ := st.Get(ch, "100.1"); tk != nil {
		t.Fatal("done task past retention should be swept")
	}
	for _, ts := range []string{"100.2", "100.3"} {
		if tk, _ := st.Get(ch, ts); tk == nil {
			t.Fatalf("%s: open or recently done task must survive", ts)
		}
	}
	f.history["100.1"] = slack.Message{Msg: slack.Msg{User: "UREPORT01", Timestamp: "100.1", Text: "old"}}
	f.reset()
	b.Handle(react("UBOB00001", "white_check_mark", "100.1", true))
	b.Handle(react("UBOB00001", "raising_hand", "100.1", true))
	if tk, _ := st.Get(ch, "100.1"); tk != nil || len(f.calls) != 0 {
		t.Fatalf("a swept task must not be adopted again, calls: %+v", f.calls)
	}
	recent := fmt.Sprintf("%d.000100", b.now().Add(-24*time.Hour).Unix())
	f.history[recent] = slack.Message{Msg: slack.Msg{User: "UREPORT01", Timestamp: recent, Text: "missed"}}
	b.Handle(react("UBOB00001", "raising_hand", recent, true))
	if tk, _ := st.Get(ch, recent); tk == nil {
		t.Fatal("a message inside the retention window must still be adopted")
	}

	b.cfg.DoneRetainDays = -1
	b.now = func() time.Time { return start.Add(365 * 24 * time.Hour) }
	b.sweepDone()
	if tk, _ := st.Get(ch, "100.3"); tk == nil {
		t.Fatal("done_retain_days -1 must keep done tasks")
	}
}

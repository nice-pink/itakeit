package bot

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/nice-pink/itakeit/pkg/config"
	"github.com/nice-pink/itakeit/pkg/store"
	"github.com/nice-pink/itakeit/pkg/task"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

const ch, ch2 = "CTASKS001", "CTASKS002"

type call struct{ kind, channel, ts, thread, user, text string }

// fakeAPI records every Slack call and hands out sequential timestamps.
type fakeAPI struct {
	calls   []call
	seq     int
	history map[string]slack.Message
	missing map[string]bool                 // ts that UpdateMessage reports as message_not_found
	errs    []string                        // errors the next UpdateMessage calls return, in order
	held    map[string][]slack.ItemReaction // reactions.get result by message ts
	heldErr error
	posted  map[string]string // channel of every message the bot posted, by ts
	wrong   []string          // calls that named a message in the wrong channel
}

// at records a call on message ts in channel c that the bot posted elsewhere.
func (f *fakeAPI) at(kind, c, ts string) {
	if p, ok := f.posted[ts]; ok && p != c {
		f.wrong = append(f.wrong, fmt.Sprintf("%s %s in %s, posted in %s", kind, ts, c, p))
	}
}

func decode(opts []slack.MsgOption) (text, thread string) {
	_, v, _ := slack.UnsafeApplyMsgOptions("", "", "", opts...)
	return v.Get("text"), v.Get("thread_ts")
}

func (f *fakeAPI) next() string { f.seq++; return fmt.Sprintf("900.%d", f.seq) }

func (f *fakeAPI) PostMessage(c string, o ...slack.MsgOption) (string, string, error) {
	text, thread := decode(o)
	ts := f.next()
	f.posted[ts] = c
	f.calls = append(f.calls, call{kind: "post", channel: c, ts: ts, thread: thread, text: text})
	return c, ts, nil
}

func (f *fakeAPI) UpdateMessage(c, ts string, o ...slack.MsgOption) (string, string, string, error) {
	f.at("update", c, ts)
	if f.missing[ts] {
		return "", "", "", slack.SlackErrorResponse{Err: "message_not_found"}
	}
	if len(f.errs) > 0 {
		e := f.errs[0]
		f.errs = f.errs[1:]
		return "", "", "", slack.SlackErrorResponse{Err: e}
	}
	text, _ := decode(o)
	f.calls = append(f.calls, call{kind: "update", channel: c, ts: ts, text: text})
	return c, ts, text, nil
}

func (f *fakeAPI) DeleteMessage(c, ts string) (string, string, error) {
	f.at("delete", c, ts)
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
	// A history entry that names its channel is found only in that channel.
	if m, ok := f.history[p.Latest]; ok && (m.Channel == "" || m.Channel == p.ChannelID) {
		r.Messages = []slack.Message{m}
	}
	return r, nil
}

func (f *fakeAPI) AddPin(c string, item slack.ItemRef) error {
	f.at("pin", c, item.Timestamp)
	f.at("pin item", item.Channel, item.Timestamp)
	f.calls = append(f.calls, call{kind: "pin", channel: c, ts: item.Timestamp})
	return nil
}

func (f *fakeAPI) GetReactions(item slack.ItemRef, _ slack.GetReactionsParameters) (slack.ReactedItem, error) {
	f.calls = append(f.calls, call{kind: "reactions", channel: item.Channel, ts: item.Timestamp})
	return slack.ReactedItem{Reactions: f.held[item.Timestamp]}, f.heldErr
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

// updated reports whether message ts in channel was edited to contain text.
func (f *fakeAPI) updated(channel, ts, text string) bool {
	for _, c := range f.calls {
		if c.kind == "update" && c.channel == channel && c.ts == ts && strings.Contains(c.text, text) {
			return true
		}
	}
	return false
}

func setup(t *testing.T) (*Bot, *fakeAPI, *store.Store) {
	t.Helper()
	cfg, err := config.Parse([]byte("channels: [" + ch + ", " + ch2 + "]\nstale_after_hours: 24\ndone_retain_days: -1"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fakeAPI{history: map[string]slack.Message{}, missing: map[string]bool{}, posted: map[string]string{}}
	t.Cleanup(func() {
		for _, w := range f.wrong {
			t.Error(w)
		}
	})
	b := New(f, st, cfg, "UBOT00001", "BBOT00001")
	now := time.Unix(1_700_000_000, 0)
	b.now = func() time.Time { return now }
	return b, f, st
}

// msg wraps a message event, in ch unless the event names another channel.
func msg(ev *slackevents.MessageEvent) slackevents.EventsAPIEvent {
	if ev.Channel == "" {
		ev.Channel = ch
	}
	return slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{Data: ev}}
}

func react(user, emoji, ts string, added bool) slackevents.EventsAPIEvent {
	return reactIn(ch, user, emoji, ts, added)
}

func reactIn(channel, user, emoji, ts string, added bool) slackevents.EventsAPIEvent {
	item := slackevents.Item{Type: "message", Channel: channel, Timestamp: ts}
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
	b.refreshBoard(ch)
	old, _ := st.KV(boardKey(ch))
	f.missing[old] = true
	f.reset()
	b.refreshBoard(ch)
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

func TestChannelsAreSeparate(t *testing.T) {
	b, f, st := setup(t)
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "api down"}))
	b.Handle(msg(&slackevents.MessageEvent{Channel: ch2, User: "UREPORT02", TimeStamp: "100.1", Text: "printer jammed"}))
	if ts, _ := st.KV(boardKey(ch2)); ts == "" {
		t.Fatal("a new task must post its own channel's board")
	}
	b.Handle(reactIn(ch2, "UBOB00001", "raising_hand", "100.1", true))
	boardB, _ := st.KV(boardKey(ch2))
	if !f.updated(ch2, boardB, "<@UBOB00001>") {
		t.Fatalf("a claim in ch2 must refresh ch2's board, calls: %+v", f.calls)
	}

	a, _ := st.Get(ch, "100.1")
	c, _ := st.Get(ch2, "100.1")
	if a == nil || c == nil || a.Text != "api down" || c.Text != "printer jammed" {
		t.Fatalf("same ts in two channels must be two tasks, got %+v and %+v", a, c)
	}
	if !strings.Contains(c.Permalink, ch2) {
		t.Fatalf("the permalink must point into the task's channel: %q", c.Permalink)
	}
	if len(a.Owners) != 0 || !c.IsOwner("UBOB00001") {
		t.Fatalf("a claim in one channel must not touch the other, got %+v and %+v", a, c)
	}
	boardA, _ := st.KV(boardKey(ch))
	if boardA == "" || boardB == "" || boardA == boardB {
		t.Fatalf("each channel needs its own board, got %q and %q", boardA, boardB)
	}
	// The last edit of each board is its current content.
	b.refreshBoards()
	var textA, textB string
	for _, cl := range f.calls {
		switch {
		case cl.kind == "update" && cl.ts == boardA && cl.channel == ch:
			textA = cl.text
		case cl.kind == "update" && cl.ts == boardB && cl.channel == ch2:
			textB = cl.text
		case cl.kind == "update" && (cl.ts == boardA || cl.ts == boardB):
			t.Fatalf("a board edit went to the wrong channel: %+v", cl)
		}
	}
	if !strings.Contains(textA, "api down") || strings.Contains(textA, "printer") ||
		!strings.Contains(textB, "printer") || strings.Contains(textB, "api down") {
		t.Fatalf("each board must list only its channel's tasks, got %q and %q", textA, textB)
	}
	pins := map[string]bool{}
	for _, cl := range f.calls {
		if cl.kind == "pin" {
			pins[cl.channel+"/"+cl.ts] = true
		}
		if cl.kind == "post" && cl.thread == "100.1" && strings.Contains(cl.text, "unclaimed") && cl.channel != ch && cl.channel != ch2 {
			t.Fatalf("card posted outside the task channels: %+v", cl)
		}
	}
	if !pins[ch+"/"+boardA] || !pins[ch2+"/"+boardB] {
		t.Fatalf("each board must be pinned in its own channel, pins: %v", pins)
	}

	f.history["200.1"] = slack.Message{Msg: slack.Msg{Channel: "COTHER001", User: "UREPORT01", Timestamp: "200.1", Text: "chatter"}}
	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{Channel: "COTHER001", User: "UREPORT01", TimeStamp: "200.1", Text: "chatter"}))
	b.Handle(reactIn("COTHER001", "UBOB00001", "raising_hand", "200.1", true))
	other := &task.Task{Channel: "COTHER001", TS: "200.2", Reporter: "UREPORT01", Text: "x\n[ ] a"}
	st.Save(other)
	cb := tick("UBOB00001", "check:200.2:"+other.Checklist()[0].Key, other.Checklist()[0].Key)
	cb.Channel.ID = "COTHER001"
	b.HandleInteraction(cb)
	if len(f.calls) != 0 {
		t.Fatalf("an unlisted channel must be ignored, calls: %+v", f.calls)
	}

	// Every handler must act on the event's channel. Each step below uses a ts that
	// also exists in ch, so falling back to one channel fails the check.
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.2", Text: "x\n[ ] a"}))
	b.Handle(msg(&slackevents.MessageEvent{Channel: ch2, User: "UREPORT02", TimeStamp: "100.2", Text: "x\n[ ] a"}))
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.3", Text: "gone soon"}))
	b.Handle(msg(&slackevents.MessageEvent{Channel: ch2, User: "UREPORT02", TimeStamp: "100.3", Text: "gone soon"}))

	f.history["300.1"] = slack.Message{Msg: slack.Msg{Channel: ch2, User: "UOLD00001", Timestamp: "300.1", Text: "old"}}
	b.Handle(reactIn(ch2, "UBOB00001", "raising_hand", "300.1", true))
	if tk, _ := st.Get(ch2, "300.1"); tk == nil {
		t.Fatal("adopt must look up history in the reacted channel")
	}

	f.reset()
	b.Handle(reactIn(ch2, "UMALLORY1", "white_check_mark", "100.1", true))
	if c := f.find("ephemeral", "Only owners"); c == nil || c.channel != ch2 {
		t.Fatalf("the owner hint must go to the reacted channel, calls: %+v", f.calls)
	}

	b.Handle(reactIn(ch2, "UBOB00001", "question", "100.1", true))
	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{Channel: ch2, User: "UREPORT02", TimeStamp: "100.9", ThreadTimeStamp: "100.1", Text: "more"}))
	if c := f.find("post", "<@UBOB00001>: <@UREPORT02> added details"); c == nil || c.channel != ch2 {
		t.Fatalf("a reply must reach the task in its own channel, calls: %+v", f.calls)
	}
	if !f.updated(ch2, boardB, "claimed") {
		t.Fatalf("a reply in ch2 must refresh ch2's board, calls: %+v", f.calls)
	}

	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{Channel: ch2, SubType: "message_changed", Message: &slack.Msg{Timestamp: "100.1", Text: "printer on fire"}}))
	if !f.updated(ch2, boardB, "printer on fire") {
		t.Fatalf("an edit in ch2 must refresh ch2's board, calls: %+v", f.calls)
	}
	a, _ = st.Get(ch, "100.1")
	c, _ = st.Get(ch2, "100.1")
	if a.Text != "api down" || c.Text != "printer on fire" {
		t.Fatalf("an edit must change only its channel's task, got %q and %q", a.Text, c.Text)
	}

	c, _ = st.Get(ch2, "100.2")
	key := c.Checklist()[0].Key
	cb = tick("UBOB00001", "check:100.2:"+key, key)
	cb.Channel.ID = ch2
	f.reset()
	b.HandleInteraction(cb)
	a, _ = st.Get(ch, "100.2")
	c, _ = st.Get(ch2, "100.2")
	if a.Checklist()[0].Checked || !c.Checklist()[0].Checked {
		t.Fatal("a tick must change only its channel's task")
	}
	if !f.updated(ch2, boardB, "1/1") {
		t.Fatalf("a tick in ch2 must refresh ch2's board, calls: %+v", f.calls)
	}

	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{Channel: ch2, SubType: "message_changed",
		Message: &slack.Msg{SubType: "tombstone", Timestamp: "100.3", Text: "This message was deleted."}}))
	if a, _ := st.Get(ch, "100.3"); a == nil {
		t.Fatal("a tombstone in one channel must not remove the other's task")
	}
	if c, _ := st.Get(ch2, "100.3"); c != nil {
		t.Fatal("the tombstoned task should be gone")
	}
	if !f.updated(ch2, boardB, "open") || f.find("delete", "") == nil {
		t.Fatalf("a delete in ch2 must drop the card and refresh ch2's board, calls: %+v", f.calls)
	}

	start := b.now()
	f.reset()
	b.now = func() time.Time { return start.Add(25 * time.Hour) }
	b.remindStale()
	if c := f.find("post", "Still on it?"); c == nil || c.channel != ch2 {
		t.Fatalf("stale reminders must cover every channel, calls: %+v", f.calls)
	}

	b.cfg.DoneRetainDays = 7
	b.Handle(reactIn(ch2, "UBOB00001", "white_check_mark", "300.1", true))
	b.now = func() time.Time { return start.Add(9 * 24 * time.Hour) }
	b.sweepDone()
	if c, _ := st.Get(ch2, "300.1"); c != nil {
		t.Fatal("the sweep must cover every channel")
	}
	b.cfg.DoneRetainDays = -1

	b.cfg.StatusClaims = true
	f.reset()
	b.Handle(reactIn(ch2, "UBOB00001", "raising_hand", "100.1", false))
	if c := f.find("reactions", ""); c == nil || c.channel != ch2 {
		t.Fatalf("reactions must be read in the task's channel, calls: %+v", f.calls)
	}
	b.cfg.StatusClaims = false

	f.reset()
	b.Handle(msg(&slackevents.MessageEvent{Channel: ch2, SubType: "message_deleted", DeletedTimeStamp: "100.1"}))
	if a, _ := st.Get(ch, "100.1"); a == nil {
		t.Fatal("a delete in one channel must not remove the other's task")
	}
	if c, _ := st.Get(ch2, "100.1"); c != nil {
		t.Fatal("the deleted task should be gone")
	}
}

func TestRefreshBoardsCoversEveryChannel(t *testing.T) {
	b, _, st := setup(t)
	b.refreshBoards()
	for _, c := range []string{ch, ch2} {
		if ts, _ := st.KV(boardKey(c)); ts == "" {
			t.Fatalf("%s has no board after start", c)
		}
	}
}

func TestBoardFillFailureDeletesPlaceholder(t *testing.T) {
	b, f, st := setup(t)
	f.errs = []string{"ratelimited"}
	b.refreshBoard(ch2)
	placeholder := f.find("post", "loading board")
	if c := f.find("delete", ""); placeholder == nil || c == nil || c.ts != placeholder.ts || c.channel != ch2 {
		t.Fatalf("an unfilled placeholder must be deleted from its channel, calls: %+v", f.calls)
	}
	if ts, _ := st.KV(boardKey(ch2)); ts != "" {
		t.Fatalf("an unfilled board must not be saved, got %q", ts)
	}
}

// tick simulates a click on the checkbox group of card block blockID, with the
// given option values ticked afterwards.
func tick(user, blockID string, ticked ...string) slack.InteractionCallback {
	a := &slack.BlockAction{ActionID: checkAction, BlockID: blockID}
	for _, v := range ticked {
		a.SelectedOptions = append(a.SelectedOptions, slack.OptionBlockObject{Value: v})
	}
	cb := slack.InteractionCallback{Type: slack.InteractionTypeBlockActions, User: slack.User{ID: user}}
	cb.Channel.ID = ch
	cb.ActionCallback.BlockActions = []*slack.BlockAction{a}
	return cb
}

func TestChecklist(t *testing.T) {
	b, f, st := setup(t)
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "deploy v2\n• [ ] migrate db\n• [ ] flip flag"}))
	card := f.find("post", "Checklist:* 0/2")
	if card == nil {
		t.Fatalf("card should show progress, calls: %+v", f.calls)
	}
	tk, _ := st.Get(ch, "100.1")
	items := tk.Checklist()
	blockA, blockB := "check:100.1:"+items[0].Key, "check:100.1:"+items[1].Key
	raw, _ := json.Marshal(cardBlocks(tk, b.emoji, false))
	if !strings.Contains(string(raw), `"type":"checkboxes"`) || !strings.Contains(string(raw), `"block_id":"`+blockB+`"`) {
		t.Fatalf("card blocks should carry one checkbox group per item: %s", raw)
	}
	b.Handle(react("UALICE001", "raising_hand", "100.1", true))

	f.reset()
	b.HandleInteraction(tick("UBYSTAND1", blockA, items[0].Key))
	if c := f.find("update", "Checklist:* 1/2"); c == nil || c.ts != card.ts {
		t.Fatalf("anyone's tick should update the card, calls: %+v", f.calls)
	}
	if f.find("update", "· 1/2 ·") == nil {
		t.Fatalf("board should show progress, calls: %+v", f.calls)
	}

	// Reordering the message keeps the tick, because keys follow the item text.
	b.Handle(msg(&slackevents.MessageEvent{SubType: "message_changed", Message: &slack.Msg{Timestamp: "100.1", Text: "deploy v2\n- [ ] flip flag\n- [ ] migrate db"}}))
	tk, _ = st.Get(ch, "100.1")
	if got := tk.Checklist(); got[1].Text != "migrate db" || !got[1].Checked || got[0].Checked {
		t.Fatalf("tick lost on reorder: %+v", got)
	}

	f.reset()
	b.HandleInteraction(tick("UALICE001", blockB, items[1].Key))
	if f.find("post", "<@UALICE001>: every checklist item is ticked. React :white_check_mark:") == nil {
		t.Fatalf("completing the checklist should nudge the owners, calls: %+v", f.calls)
	}
	f.reset()
	b.HandleInteraction(tick("UALICE001", blockB, items[1].Key))
	if f.find("post", "every checklist item") != nil {
		t.Fatalf("an unchanged click must not nudge again, calls: %+v", f.calls)
	}
}

func TestChecklistStaleViewKeepsOtherTicks(t *testing.T) {
	b, _, st := setup(t)
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "x\n[ ] a\n[ ] b"}))
	tk, _ := st.Get(ch, "100.1")
	a, bk := tk.Checklist()[0].Key, tk.Checklist()[1].Key
	b.HandleInteraction(tick("UONE00001", "check:100.1:"+a, a))
	// UTWO's card still shows a unticked; clicking b reports only b.
	b.HandleInteraction(tick("UTWO00001", "check:100.1:"+bk, bk))
	tk, _ = st.Get(ch, "100.1")
	if got := tk.Checklist(); !got[0].Checked || !got[1].Checked {
		t.Fatalf("a click on one item must not untick another: %+v", got)
	}
}

func TestCardBlockLimits(t *testing.T) {
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("[ ] item %d", i))
	}
	tk := &task.Task{TS: "100.1", Text: strings.Join(lines, "\n")}
	if n := len(cardBlocks(tk, nil, false)); n > 50 {
		t.Fatalf("card has %d blocks, Block Kit allows 50", n)
	}
	if got := optionText(strings.Repeat("🚀", 100)); len(utf16.Encode([]rune(got))) > 75 {
		t.Fatalf("option text %d UTF-16 units, limit 75", len(utf16.Encode([]rune(got))))
	}
}

func TestCardFallback(t *testing.T) {
	b, f, st := setup(t)
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "x\n[ ] a"}))
	tk, _ := st.Get(ch, "100.1")

	f.reset()
	f.errs = []string{"ratelimited"}
	b.updateCard(tk)
	if len(f.calls) != 0 {
		t.Fatalf("a transient error must not trigger the text-only fallback, calls: %+v", f.calls)
	}
	f.errs = []string{"invalid_blocks"}
	b.updateCard(tk)
	if c := f.find("update", "Checklist:* 0/1"); c == nil || c.ts != tk.CardTS || len(f.calls) != 1 {
		t.Fatalf("rejected blocks should fall back to a text-only edit of the same card, calls: %+v", f.calls)
	}
}

func TestStatusClaims(t *testing.T) {
	b, f, st := setup(t)
	b.cfg.StatusClaims = true
	b.Handle(msg(&slackevents.MessageEvent{User: "UREPORT01", TimeStamp: "100.1", Text: "db down"}))
	if f.find("post", "Any of these makes you an owner") == nil {
		t.Fatalf("card should explain the rule, calls: %+v", f.calls)
	}

	f.reset()
	b.Handle(react("UALICE001", "construction", "100.1", true))
	tk, _ := st.Get(ch, "100.1")
	if f.find("ephemeral", "") != nil || !tk.IsOwner("UALICE001") || tk.Status != task.InProgress {
		t.Fatalf("a status reaction should own and set the status, got %+v calls %+v", tk, f.calls)
	}

	// Switching status: the new reaction lands before the old one is removed.
	b.Handle(react("UALICE001", "eyes", "100.1", true))
	f.held = map[string][]slack.ItemReaction{"100.1": {{Name: "eyes", Users: []string{"UALICE001"}}, {Name: "thumbsup", Users: []string{"UBOB00001"}}}}
	b.Handle(react("UALICE001", "construction", "100.1", false))
	if tk, _ = st.Get(ch, "100.1"); !tk.IsOwner("UALICE001") || tk.Status != task.Investigating {
		t.Fatalf("still holding a status keeps ownership, got %+v", tk)
	}

	f.held = map[string][]slack.ItemReaction{"100.1": {{Name: "eyes", Users: []string{"UALICE001"}}, {Name: "thumbsup", Users: []string{"UALICE001"}}}}
	b.Handle(react("UALICE001", "eyes", "100.1", false))
	if tk, _ = st.Get(ch, "100.1"); len(tk.Owners) != 0 || tk.Status != "" {
		t.Fatalf("removing the last status reaction hands the task back, even if reactions.get still lists it, got %+v", tk)
	}

	b.Handle(react("UBOB00001", "eyes", "100.1", true))
	f.heldErr = fmt.Errorf("ratelimited")
	b.Handle(react("UBOB00001", "raising_hand", "100.1", false))
	if tk, _ = st.Get(ch, "100.1"); !tk.IsOwner("UBOB00001") {
		t.Fatalf("an unanswered reactions lookup keeps the owner, got %+v", tk)
	}
}

// Package bot wires Slack Socket Mode events to task state.
//
// All events and timers are handled on one goroutine, so state changes never
// race and the store needs no locking beyond SQLite's own.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/nice-pink/itakeit/pkg/config"
	"github.com/nice-pink/itakeit/pkg/store"
	"github.com/nice-pink/itakeit/pkg/task"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func boardKey(channel string) string { return "board_ts:" + channel }

// checkAction is the action_id of the checklist checkboxes on a card.
const checkAction = "checklist"

// API is the subset of *slack.Client the bot uses.
type API interface {
	PostMessage(channel string, opts ...slack.MsgOption) (string, string, error)
	UpdateMessage(channel, ts string, opts ...slack.MsgOption) (string, string, string, error)
	DeleteMessage(channel, ts string) (string, string, error)
	PostEphemeral(channel, user string, opts ...slack.MsgOption) (string, error)
	GetPermalink(p *slack.PermalinkParameters) (string, error)
	GetConversationHistory(p *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
	AddPin(channel string, item slack.ItemRef) error
}

type Bot struct {
	api       API
	store     *store.Store
	cfg       *config.Config
	emoji     map[task.Action]string
	botUserID string
	botID     string
	now       func() time.Time
}

func New(api API, st *store.Store, cfg *config.Config, botUserID, botID string) *Bot {
	return &Bot{api: api, store: st, cfg: cfg, emoji: cfg.Display(), botUserID: botUserID, botID: botID, now: time.Now}
}

// Run consumes Socket Mode events until ctx is cancelled.
func (b *Bot) Run(ctx context.Context, sm *socketmode.Client) error {
	errc := make(chan error, 1)
	go func() { errc <- sm.RunContext(ctx) }()
	events, interactions := ackLoop(ctx, sm)

	b.refreshBoard()
	tick := time.NewTicker(staleInterval(b.cfg.StaleAfter()))
	defer tick.Stop()
	var sweep <-chan time.Time // nil, never fires, when cleanup is off
	if b.cfg.DoneRetain() > 0 {
		b.sweepDone()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		sweep = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			return err
		case <-tick.C:
			b.remindStale()
		case <-sweep:
			b.sweepDone()
		case e := <-events:
			b.Handle(e)
		case cb := <-interactions:
			b.HandleInteraction(cb)
		}
	}
}

// ackLoop acknowledges every envelope the moment it arrives, so Slack's 3 s ack
// deadline never depends on how long handling takes, and queues the events and
// interactions for the single handler goroutine.
func ackLoop(ctx context.Context, sm *socketmode.Client) (<-chan slackevents.EventsAPIEvent, <-chan slack.InteractionCallback) {
	out := make(chan slackevents.EventsAPIEvent, 1024)
	clicks := make(chan slack.InteractionCallback, 1024)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-sm.Events:
				switch evt.Type {
				case socketmode.EventTypeConnecting:
					slog.Info("connecting to slack")
				case socketmode.EventTypeConnected:
					slog.Info("connected to slack")
				case socketmode.EventTypeConnectionError:
					slog.Warn("slack connection error, retrying", "data", evt.Data)
				case socketmode.EventTypeEventsAPI:
					if evt.Request != nil {
						sm.Ack(*evt.Request)
					}
					e, ok := evt.Data.(slackevents.EventsAPIEvent)
					if !ok {
						continue
					}
					select {
					case out <- e:
					default:
						slog.Error("event queue full, dropping event", "type", e.InnerEvent.Type)
					}
				case socketmode.EventTypeInteractive:
					if evt.Request != nil {
						sm.Ack(*evt.Request)
					}
					cb, ok := evt.Data.(slack.InteractionCallback)
					if !ok {
						continue
					}
					select {
					case clicks <- cb:
					default:
						slog.Error("interaction queue full, dropping", "type", cb.Type)
					}
				}
			}
		}
	}()
	return out, clicks
}

func staleInterval(after time.Duration) time.Duration {
	return min(max(after/8, time.Minute), 15*time.Minute)
}

// Handle dispatches one Events API event. Exported for tests.
func (b *Bot) Handle(e slackevents.EventsAPIEvent) {
	switch ev := e.InnerEvent.Data.(type) {
	case *slackevents.MessageEvent:
		b.onMessage(ev)
	case *slackevents.ReactionAddedEvent:
		b.onReaction(ev.User, ev.Reaction, ev.Item, true)
	case *slackevents.ReactionRemovedEvent:
		b.onReaction(ev.User, ev.Reaction, ev.Item, false)
	}
}

// HandleInteraction applies checklist ticks from a card. Exported for tests.
func (b *Bot) HandleInteraction(cb slack.InteractionCallback) {
	if cb.Type != slack.InteractionTypeBlockActions || cb.Channel.ID != b.cfg.Channel {
		return
	}
	for _, a := range cb.ActionCallback.BlockActions {
		if a.ActionID == checkAction {
			b.onCheck(cb.User.ID, a)
		}
	}
}

// onCheck applies one click. Every item is its own checkbox element, so a click
// reports exactly one item and a stale view of the others cannot untick them.
// The block ID is "check:<task ts>:<item key>".
func (b *Bot) onCheck(user string, a *slack.BlockAction) {
	parts := strings.SplitN(a.BlockID, ":", 3)
	if len(parts) != 3 || parts[0] != "check" {
		return
	}
	t := b.get(parts[1])
	if t == nil {
		return // swept or unknown: the card is frozen
	}
	key := parts[2]
	on := slices.ContainsFunc(a.SelectedOptions, func(o slack.OptionBlockObject) bool { return o.Value == key })
	eff := t.Check(user, map[string]bool{key: on}, b.now())
	if eff == task.NoChange {
		b.updateCard(t) // the clicker's view may be stale or show a removed item
		return
	}
	b.save(t)
	b.updateCard(t)
	b.refreshBoard()
	if eff == task.ChecklistDone {
		who := task.Mention(t.Reporter)
		if len(t.Owners) > 0 {
			who = task.Mentions(t.Owners)
		}
		msg := fmt.Sprintf("%s: every checklist item is ticked.", who)
		if e, ok := b.emoji[task.Done]; ok {
			msg += fmt.Sprintf(" React :%s: on the task when it is done.", e)
		}
		b.say(t, msg)
	}
}

func (b *Bot) onMessage(ev *slackevents.MessageEvent) {
	if ev.Channel != b.cfg.Channel {
		return
	}
	switch ev.SubType {
	case "message_changed":
		m := ev.Message
		if m == nil || !isRoot(m.ThreadTimestamp, m.Timestamp) {
			return
		}
		// A deleted message that has replies (every task has its card) stays as a
		// tombstone and arrives as message_changed, not message_deleted.
		if m.SubType == "tombstone" {
			b.onDelete(m.Timestamp)
			return
		}
		b.onEdit(m.Timestamp, m.Text)
	case "message_deleted":
		b.onDelete(ev.DeletedTimeStamp)
	case "", "bot_message", "file_share", "thread_broadcast":
		if b.own(ev.User, ev.BotID) {
			return
		}
		if !isRoot(ev.ThreadTimeStamp, ev.TimeStamp) {
			b.onReply(ev.ThreadTimeStamp, ev.User)
			return
		}
		if b.get(ev.TimeStamp) != nil {
			return // redelivered event
		}
		b.create(ev.TimeStamp, reporter(ev.User, ev.Username, ev.BotID), ev.Text)
	}
}

func (b *Bot) create(ts, reporter, text string) *task.Task {
	now := b.now()
	t := &task.Task{Channel: b.cfg.Channel, TS: ts, Reporter: reporter, Text: text, CreatedAt: now, LastActivity: now}
	link, err := b.api.GetPermalink(&slack.PermalinkParameters{Channel: t.Channel, Ts: ts})
	if err != nil {
		slog.Warn("permalink", "ts", ts, "err", err)
	}
	t.Permalink = link
	b.save(t)
	b.updateCard(t)
	b.refreshBoard()
	slog.Info("task created", "ts", ts, "reporter", reporter)
	return t
}

func (b *Bot) onReply(threadTS, user string) {
	t := b.get(threadTS)
	if t == nil {
		return
	}
	eff := t.Reply(user, b.now())
	b.save(t)
	if eff == task.NotifyOwners {
		b.say(t, fmt.Sprintf("%s: %s added details.", task.Mentions(t.Owners), task.Mention(t.Reporter)))
		b.updateCard(t)
		b.refreshBoard()
	}
}

func (b *Bot) onEdit(ts, text string) {
	t := b.get(ts)
	if t == nil || t.Text == text {
		return
	}
	before := t.Checklist()
	t.SetText(text)
	b.save(t)
	if !slices.Equal(before, t.Checklist()) {
		b.updateCard(t)
	}
	b.refreshBoard()
}

func (b *Bot) onDelete(ts string) {
	t := b.get(ts)
	if t == nil {
		return
	}
	if t.CardTS != "" {
		if _, _, err := b.api.DeleteMessage(t.Channel, t.CardTS); err != nil {
			slog.Warn("delete card", "ts", ts, "err", err)
		}
	}
	if err := b.store.Delete(t.Channel, ts); err != nil {
		slog.Error("delete task", "ts", ts, "err", err)
	}
	b.refreshBoard()
}

func (b *Bot) onReaction(user, reaction string, item slackevents.Item, added bool) {
	if item.Type != "message" || item.Channel != b.cfg.Channel || user == b.botUserID {
		return
	}
	a, ok := b.cfg.Action(reaction)
	if !ok {
		return
	}
	t := b.get(item.Timestamp)
	if t == nil && added {
		t = b.adopt(item.Timestamp)
	}
	if t == nil {
		return
	}
	switch t.React(a, user, added, b.now()) {
	case task.NoChange:
		return
	case task.Denied:
		msg := fmt.Sprintf("Only owners can set a status. React :%s: on the task first.", b.emoji[task.Claim])
		if _, err := b.api.PostEphemeral(t.Channel, user, slack.MsgOptionText(msg, false), slack.MsgOptionTS(t.TS)); err != nil {
			slog.Warn("ephemeral", "err", err)
		}
		return
	case task.AskReporter:
		b.say(t, fmt.Sprintf("%s: %s needs more details. Please reply in this thread.", task.Mention(t.Reporter), task.Mention(user)))
	}
	b.save(t)
	b.updateCard(t)
	b.refreshBoard()
}

// adopt turns a top-level message that predates the bot (or was missed while it
// was offline) into a task when someone reacts to it.
//
// Messages older than done_retain_days are never adopted: every swept task is
// that old, and adopting one would reopen finished work under a second card.
func (b *Bot) adopt(ts string) *task.Task {
	if r := b.cfg.DoneRetain(); r > 0 {
		if sec, err := strconv.ParseFloat(ts, 64); err == nil && b.now().Sub(time.Unix(int64(sec), 0)) >= r {
			return nil
		}
	}
	h, err := b.api.GetConversationHistory(&slack.GetConversationHistoryParameters{
		ChannelID: b.cfg.Channel, Latest: ts, Oldest: ts, Inclusive: true, Limit: 1})
	if err != nil {
		slog.Warn("adopt: history", "ts", ts, "err", err)
		return nil
	}
	if len(h.Messages) == 0 {
		return nil // a thread reply, or gone
	}
	m := h.Messages[0]
	if m.Timestamp != ts || !isRoot(m.ThreadTimestamp, m.Timestamp) || b.own(m.User, m.BotID) {
		return nil
	}
	switch m.SubType {
	case "", "bot_message", "file_share":
	default:
		return nil
	}
	return b.create(ts, reporter(m.User, m.Username, m.BotID), m.Text)
}

func (b *Bot) remindStale() {
	open, err := b.store.Open(b.cfg.Channel)
	if err != nil {
		slog.Error("list open", "err", err)
		return
	}
	now := b.now()
	for i := range open {
		t := &open[i]
		if !t.Stale(now, b.cfg.StaleAfter()) {
			continue
		}
		b.say(t, fmt.Sprintf("%s: no update here for %s. Still on it? Post a status here, or remove your :%s: to hand it back.",
			task.Mentions(t.Owners), hours(now.Sub(t.LastActivity)), b.emoji[task.Claim]))
		t.RemindedAt = now
		b.save(t)
	}
}

// sweepDone deletes done tasks idle for longer than done_retain_days. Only the
// database row goes: the message and card stay in Slack, and the task is frozen
// because adopt refuses messages that old.
func (b *Bot) sweepDone() {
	if b.cfg.DoneRetain() <= 0 {
		return
	}
	n, err := b.store.DeleteDone(b.cfg.Channel, b.now().Add(-b.cfg.DoneRetain()))
	if err != nil {
		slog.Error("sweep done", "err", err)
		return
	}
	if n > 0 {
		slog.Info("swept done tasks", "count", n)
	}
}

func (b *Bot) updateCard(t *task.Task) {
	// If Slack rejects the checklist blocks, fall back to the plain-text card, so
	// a checklist can never cost a task its card. Other errors (rate limits,
	// timeouts) must not strip the checkboxes or post a second card.
	if err := b.putCard(t, cardBlocks(t, b.emoji)); err != nil && strings.HasPrefix(err.Error(), "invalid_blocks") {
		b.putCard(t, nil)
	}
}

// putCard edits or posts the card. A nil blocks renders text only and clears
// blocks on an edit.
func (b *Bot) putCard(t *task.Task, blocks []slack.Block) error {
	opts := []slack.MsgOption{slack.MsgOptionText(task.Card(*t, b.emoji), false), slack.MsgOptionBlocks(blocks...)}
	if t.CardTS != "" {
		_, _, _, err := b.api.UpdateMessage(t.Channel, t.CardTS, opts...)
		if err == nil || err.Error() != "message_not_found" {
			if err != nil {
				slog.Warn("update card", "ts", t.TS, "err", err)
			}
			return err
		}
	}
	if blocks == nil {
		opts = opts[:1]
	}
	_, ts, err := b.api.PostMessage(t.Channel, append(opts, slack.MsgOptionTS(t.TS))...)
	if err != nil {
		slog.Warn("post card", "ts", t.TS, "err", err)
		return err
	}
	t.CardTS = ts
	b.save(t)
	return nil
}

// maxCardItems keeps the card within Block Kit's 50 blocks: the card text, one
// actions block per item, and a note when items are left off.
const maxCardItems = 48

// cardBlocks renders the card text, then one single-option checkbox group per
// checklist item.
func cardBlocks(t *task.Task, emoji map[task.Action]string) []slack.Block {
	blocks := []slack.Block{slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, task.Card(*t, emoji), false, false), nil, nil)}
	items := t.Checklist()
	for _, it := range items[:min(len(items), maxCardItems)] {
		o := slack.NewOptionBlockObject(it.Key, slack.NewTextBlockObject(slack.MarkdownType, optionText(it.Text), false, false), nil)
		el := slack.NewCheckboxGroupsBlockElement(checkAction, o)
		if it.Checked {
			el.InitialOptions = []*slack.OptionBlockObject{o}
		}
		blocks = append(blocks, slack.NewActionBlock("check:"+t.TS+":"+it.Key, el))
	}
	if n := len(items) - maxCardItems; n > 0 {
		blocks = append(blocks, slack.NewContextBlock("", slack.NewTextBlockObject(slack.MarkdownType,
			fmt.Sprintf("%d more checklist items are counted but not shown here.", n), false, false)))
	}
	return blocks
}

// optionText fits an item into Block Kit's 75-character option text, counted in
// UTF-16 units so emoji-heavy items stay inside the limit too.
func optionText(s string) string {
	for n := 74; ; n-- {
		if e := task.Excerpt(s, n); len(utf16.Encode([]rune(e))) <= 75 || n <= 1 {
			return e
		}
	}
}

func (b *Bot) refreshBoard() {
	open, err := b.store.Open(b.cfg.Channel)
	if err != nil {
		slog.Error("list open", "err", err)
		return
	}
	text := slack.MsgOptionText(task.Board(open, b.emoji, b.cfg.BoardMaxTasks, b.now()), false)
	ts, err := b.store.KV(boardKey(b.cfg.Channel))
	if err != nil {
		slog.Error("board ts", "err", err)
		return
	}
	if ts != "" {
		if _, _, _, err := b.api.UpdateMessage(b.cfg.Channel, ts, text, slack.MsgOptionDisableLinkUnfurl()); err == nil {
			return
		} else if err.Error() != "message_not_found" {
			slog.Warn("update board", "err", err)
			return
		}
	}
	// Post a placeholder and edit the content in: a new message would notify every
	// owner and @here in the excerpts, an edit notifies nobody.
	_, ts, err = b.api.PostMessage(b.cfg.Channel, slack.MsgOptionText("I take it: loading board…", false))
	if err != nil {
		slog.Warn("post board", "err", err)
		return
	}
	if _, _, _, err := b.api.UpdateMessage(b.cfg.Channel, ts, text, slack.MsgOptionDisableLinkUnfurl()); err != nil {
		slog.Warn("fill board", "err", err)
		b.api.DeleteMessage(b.cfg.Channel, ts) // retried on the next change
		return
	}
	if err := b.api.AddPin(b.cfg.Channel, slack.ItemRef{Channel: b.cfg.Channel, Timestamp: ts}); err != nil {
		slog.Warn("pin board", "err", err)
	}
	if err := b.store.SetKV(boardKey(b.cfg.Channel), ts); err != nil {
		slog.Error("save board ts", "err", err)
	}
}

func (b *Bot) say(t *task.Task, text string) {
	if _, _, err := b.api.PostMessage(t.Channel, slack.MsgOptionText(text, false), slack.MsgOptionTS(t.TS)); err != nil {
		slog.Warn("post thread", "ts", t.TS, "err", err)
	}
}

func (b *Bot) get(ts string) *task.Task {
	t, err := b.store.Get(b.cfg.Channel, ts)
	if err != nil {
		slog.Error("get task", "ts", ts, "err", err)
	}
	return t
}

func (b *Bot) save(t *task.Task) {
	if err := b.store.Save(t); err != nil {
		slog.Error("save task", "ts", t.TS, "err", err)
	}
}

func (b *Bot) own(user, botID string) bool {
	return (user != "" && user == b.botUserID) || (botID != "" && botID == b.botID)
}

func hours(d time.Duration) string {
	if h := int(d.Hours()); h != 1 {
		return fmt.Sprintf("%d hours", h)
	}
	return "1 hour"
}

func isRoot(threadTS, ts string) bool { return threadTS == "" || threadTS == ts }

func reporter(user, username, botID string) string {
	switch {
	case user != "":
		return user
	case username != "":
		return username
	case botID != "":
		return "bot " + botID
	}
	return "unknown"
}

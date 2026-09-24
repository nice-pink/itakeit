// Package bot wires Slack Socket Mode events to task state.
//
// All events and timers are handled on one goroutine, so state changes never
// race and the store needs no locking beyond SQLite's own.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nice-pink/itakeit/pkg/config"
	"github.com/nice-pink/itakeit/pkg/store"
	"github.com/nice-pink/itakeit/pkg/task"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func boardKey(channel string) string { return "board_ts:" + channel }

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
	events := ackLoop(ctx, sm)

	b.refreshBoard()
	tick := time.NewTicker(staleInterval(b.cfg.StaleAfter()))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			return err
		case <-tick.C:
			b.remindStale()
		case e := <-events:
			b.Handle(e)
		}
	}
}

// ackLoop acknowledges every envelope the moment it arrives, so Slack's 3 s ack
// deadline never depends on how long handling takes, and queues the events for
// the single handler goroutine.
func ackLoop(ctx context.Context, sm *socketmode.Client) <-chan slackevents.EventsAPIEvent {
	out := make(chan slackevents.EventsAPIEvent, 1024)
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
				}
			}
		}
	}()
	return out
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
	t.Text = text
	b.save(t)
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
func (b *Bot) adopt(ts string) *task.Task {
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

func (b *Bot) updateCard(t *task.Task) {
	text := slack.MsgOptionText(task.Card(*t, b.emoji), false)
	if t.CardTS != "" {
		if _, _, _, err := b.api.UpdateMessage(t.Channel, t.CardTS, text); err == nil {
			return
		} else if err.Error() != "message_not_found" {
			slog.Warn("update card", "ts", t.TS, "err", err)
			return
		}
	}
	_, ts, err := b.api.PostMessage(t.Channel, text, slack.MsgOptionTS(t.TS))
	if err != nil {
		slog.Warn("post card", "ts", t.TS, "err", err)
		return
	}
	t.CardTS = ts
	b.save(t)
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

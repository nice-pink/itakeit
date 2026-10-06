// Package bot wires Slack Socket Mode events to task state.
//
// All events and timers are handled on one goroutine, so state changes never
// race and the store needs no locking beyond the database's own.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
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
	GetReactions(item slack.ItemRef, p slack.GetReactionsParameters) (slack.ReactedItem, error)
	GetConversationsForUser(p *slack.GetConversationsForUserParameters) ([]slack.Channel, string, error)
	GetUserInfo(user string) (*slack.User, error)
}

type Bot struct {
	api       API
	store     *store.Store
	cfg       *config.Config
	emoji     map[task.Action]string
	botUserID string
	botID     string
	now       func() time.Time
	joined    map[string]bool // channels served under auto_channels
	left      map[string]bool // channels the bot was removed from, until it is invited back
}

func New(api API, st *store.Store, cfg *config.Config, botUserID, botID string) *Bot {
	return &Bot{api: api, store: st, cfg: cfg, emoji: cfg.Display(), botUserID: botUserID, botID: botID, now: time.Now, joined: map[string]bool{}, left: map[string]bool{}}
}

// Run consumes Socket Mode events until ctx is cancelled.
func (b *Bot) Run(ctx context.Context, sm *socketmode.Client) error {
	errc := make(chan error, 1)
	go func() { errc <- sm.RunContext(ctx) }()
	events, interactions := ackLoop(ctx, sm)

	b.start()
	tick := time.NewTicker(staleInterval(b.cfg.StaleAfter()))
	defer tick.Stop()
	remind := time.NewTicker(time.Minute)
	defer remind.Stop()
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
			b.rediscover()
			b.remindStale()
		case <-remind.C:
			b.sendReminders()
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
	case *slackevents.MemberJoinedChannelEvent:
		if ev.User == b.botUserID {
			b.onJoin(ev.Channel)
		}
	case *slackevents.MemberLeftChannelEvent:
		if ev.User == b.botUserID {
			b.onLeave(ev.Channel)
		}
	case *slackevents.ChannelLeftEvent:
		b.onLeave(ev.Channel)
	case *slackevents.GroupLeftEvent:
		b.onLeave(ev.Channel)
	case *slackevents.ChannelArchiveEvent:
		b.onArchive(ev.Channel)
	case *slackevents.GroupArchiveEvent:
		b.onArchive(ev.Channel)
	}
}

// start discovers channels and brings their boards up to date, before the
// event loop runs.
func (b *Bot) start() {
	b.discover()
	b.refreshBoards()
}

// serves reports whether events from channel are handled. Under auto_channels
// that is any channel a message or reaction event arrives from, because Slack
// sends those only for channels the bot is a member of, and the channel is
// remembered so one missed by discover (or joined while the bot was down) still
// gets its reminders and sweep. A channel the bot was removed from stays out
// until it is invited back: the event loop does not keep Slack's order, so a
// message sent just before the removal can be handled after it.
func (b *Bot) serves(channel string) bool {
	if !b.cfg.AutoChannels {
		return b.cfg.Serves(channel)
	}
	if !config.IsChannelID(channel) || b.left[channel] {
		return false
	}
	if !b.joined[channel] {
		b.joined[channel] = true
		slog.Info("serving channel", "channel", channel)
	}
	return true
}

// channels lists the channels whose boards, reminders and sweep the bot runs.
func (b *Bot) channels() []string {
	if !b.cfg.AutoChannels {
		return b.cfg.Channels
	}
	return slices.Sorted(maps.Keys(b.joined))
}

// discover replaces the served channels with the unarchived channels the bot
// is a member of, under auto_channels, and returns the ones that are new. The
// listing is the truth: events are handled out of order, so a late leave, a
// late message into an archived channel or a missed unarchive can leave the
// served set wrong, and every discover corrects it. A failure is logged and
// changes nothing; the next tick retries, and events still add channels.
func (b *Bot) discover() []string {
	if !b.cfg.AutoChannels {
		return nil
	}
	found := map[string]bool{}
	p := &slack.GetConversationsForUserParameters{Types: []string{"public_channel", "private_channel"}, ExcludeArchived: true, Limit: 200}
	for {
		chans, next, err := b.api.GetConversationsForUser(p)
		if err != nil {
			slog.Warn("list member channels", "err", err)
			return nil
		}
		for _, c := range chans {
			found[c.ID] = true
		}
		if next == "" {
			break
		}
		p.Cursor = next
	}
	var added []string
	for c := range found {
		if !b.joined[c] {
			added = append(added, c)
			slog.Info("serving channel", "channel", c)
		}
	}
	for c := range b.joined {
		if !found[c] {
			slog.Info("no longer a member, stopping", "channel", c)
		}
	}
	b.joined, b.left = found, map[string]bool{}
	slices.Sort(added)
	return added
}

// rediscover runs discover on the reminder tick and posts the boards of
// channels it found new.
func (b *Bot) rediscover() {
	for _, c := range b.discover() {
		b.refreshBoard(c)
	}
}

// onJoin posts the board as soon as the bot is invited to a served channel.
func (b *Bot) onJoin(channel string) {
	delete(b.left, channel)
	if !b.serves(channel) {
		slog.Info("invited to a channel the bot does not serve, ignoring it", "channel", channel)
		return
	}
	b.refreshBoard(channel)
}

// onLeave stops boards, reminders and sweep for a channel the bot was removed
// from, under auto_channels. Its rows stay; a new invite picks them up again.
func (b *Bot) onLeave(channel string) {
	if !b.cfg.AutoChannels {
		return
	}
	b.left[channel] = true
	if b.joined[channel] {
		delete(b.joined, channel)
		slog.Info("left channel", "channel", channel)
	}
}

// onArchive stops boards, reminders and sweep for an archived channel, under
// auto_channels. The bot is still a member, so a message after unarchiving
// serves it again.
func (b *Bot) onArchive(channel string) {
	if b.cfg.AutoChannels && b.joined[channel] {
		delete(b.joined, channel)
		slog.Info("channel archived", "channel", channel)
	}
}

// HandleInteraction applies checklist ticks from a card. Exported for tests.
func (b *Bot) HandleInteraction(cb slack.InteractionCallback) {
	// A click arrives even from a channel the bot has left, so it never adds one.
	served := b.cfg.Serves(cb.Channel.ID) || b.joined[cb.Channel.ID]
	if cb.Type != slack.InteractionTypeBlockActions || !served {
		return
	}
	for _, a := range cb.ActionCallback.BlockActions {
		if a.ActionID == checkAction {
			b.onCheck(cb.Channel.ID, cb.User.ID, a)
		}
	}
}

// onCheck applies one click. Every item is its own checkbox element, so a click
// reports exactly one item and a stale view of the others cannot untick them.
// The block ID is "check:<task ts>:<item key>".
func (b *Bot) onCheck(channel, user string, a *slack.BlockAction) {
	parts := strings.SplitN(a.BlockID, ":", 3)
	if len(parts) != 3 || parts[0] != "check" {
		return
	}
	t := b.get(channel, parts[1])
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
	b.refreshBoard(t.Channel)
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
	if !b.serves(ev.Channel) {
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
			b.onDelete(ev.Channel, m.Timestamp)
			return
		}
		b.onEdit(ev.Channel, m.Timestamp, m.Text)
	case "message_deleted":
		b.onDelete(ev.Channel, ev.DeletedTimeStamp)
	case "", "bot_message", "file_share", "thread_broadcast":
		if b.own(ev.User, ev.BotID) {
			return
		}
		if !isRoot(ev.ThreadTimeStamp, ev.TimeStamp) {
			b.onReply(ev.Channel, ev.ThreadTimeStamp, ev.User, ev.Text)
			return
		}
		if b.get(ev.Channel, ev.TimeStamp) != nil {
			return // redelivered event
		}
		b.create(ev.Channel, ev.TimeStamp, b.reporter(ev.Channel, ev.User, ev.Username, ev.BotID), ev.Text)
	}
}

func (b *Bot) create(channel, ts, reporter, text string) *task.Task {
	now := b.now()
	t := &task.Task{Channel: channel, TS: ts, Reporter: reporter, Text: text, CreatedAt: now, LastActivity: now}
	link, err := b.api.GetPermalink(&slack.PermalinkParameters{Channel: t.Channel, Ts: ts})
	if err != nil {
		slog.Warn("permalink", "ts", ts, "err", err)
	}
	t.Permalink = link
	b.save(t)
	b.updateCard(t)
	b.refreshBoard(t.Channel)
	slog.Info("task created", "channel", channel, "ts", ts, "reporter", reporter)
	return t
}

func (b *Bot) onReply(channel, threadTS, user, text string) {
	t := b.get(channel, threadTS)
	if t == nil {
		return
	}
	if b.onRemind(t, user, text) {
		return // a reminder request is not task activity
	}
	eff := t.Reply(user, b.now())
	b.save(t)
	if eff == task.NotifyOwners {
		b.say(t, fmt.Sprintf("%s: %s added details.", task.Mentions(t.Owners), task.Mention(t.Reporter)))
		b.updateCard(t)
		b.refreshBoard(t.Channel)
	}
}

// onRemind handles a "remind me ..." reply from anyone. It reports whether the
// reply was one, in which case it is not treated as an ordinary reply. Anything
// that starts with "remind me" but cannot be read gets a private usage hint, so
// a request never fails silently or counts as task activity.
func (b *Bot) onRemind(t *task.Task, user, text string) bool {
	if user == "" || !task.IsRemind(text) {
		return false
	}
	say := func(msg string) {
		if _, err := b.api.PostEphemeral(t.Channel, user, slack.MsgOptionText(msg, false), slack.MsgOptionTS(t.TS)); err != nil {
			slog.Warn("reminder ack", "ts", t.TS, "err", err)
		}
	}
	if !t.Open() {
		say("This task is done, so no reminder was set.")
		return true
	}
	loc := time.UTC
	if u, err := b.api.GetUserInfo(user); err != nil {
		slog.Warn("user info", "user", user, "err", err)
	} else if l, err := time.LoadLocation(u.TZ); err == nil {
		loc = l
	}
	due, ok := task.ParseRemind(text, b.now(), loc)
	if !ok {
		say("I could not read that time. Try `remind me tomorrow`, `remind me monday at 15:00`, `remind me in 3 hours` or `remind me at 15:00`.")
		return true
	}
	if err := b.store.SetReminder(store.Reminder{Channel: t.Channel, TS: t.TS, User: user, Due: due}); err != nil {
		slog.Error("set reminder", "ts", t.TS, "err", err)
		say("Could not save the reminder, try again.")
		return true
	}
	say(fmt.Sprintf("I will remind you on %s.", due.In(loc).Format("Mon 2 Jan 15:04 MST")))
	return true
}

// remindGrace is how long past its due time a reminder that cannot be
// delivered is retried before it is dropped.
const remindGrace = 6 * time.Hour

// permanentDM lists Slack errors that retrying cannot fix.
var permanentDM = []string{"user_not_found", "user_disabled", "cannot_dm_bot", "channel_not_found", "is_archived", "missing_scope", "not_authed"}

// sendReminders DMs every due reminder. Reminders of done or deleted tasks are
// dropped. A store error or a failed DM keeps the reminder for the next tick,
// except for a permanent Slack error or once remindGrace has passed. Each
// reminder is deleted before its DM goes out, so a store that cannot delete
// never causes repeated DMs; the cost is a lost reminder if requeueing fails too.
func (b *Bot) sendReminders() {
	now := b.now()
	due, err := b.store.DueReminders(now)
	if err != nil {
		slog.Error("due reminders", "err", err)
		return
	}
	for _, r := range due {
		t, err := b.store.Get(r.Channel, r.TS)
		if err != nil {
			slog.Error("get task for reminder", "ts", r.TS, "err", err)
			continue
		}
		if t == nil || !t.Open() {
			if err := b.store.DeleteReminder(r); err != nil {
				slog.Error("delete reminder", "ts", r.TS, "err", err)
			}
			continue
		}
		// Delete before sending: a store that cannot delete would otherwise
		// resend the same DM every tick. A transient send failure puts it back.
		if err := b.store.DeleteReminder(r); err != nil {
			slog.Error("delete reminder", "ts", r.TS, "err", err)
			continue
		}
		link := t.Permalink
		if link == "" {
			link = fmt.Sprintf("<#%s>", t.Channel)
		}
		text := fmt.Sprintf(":alarm_clock: You asked to be reminded of this task: %s", link)
		_, _, err = b.api.PostMessage(r.User, slack.MsgOptionText(text, false))
		if err == nil {
			continue
		}
		slog.Warn("send reminder", "ts", r.TS, "user", r.User, "err", err)
		if now.Sub(r.Due) >= remindGrace || slices.ContainsFunc(permanentDM, func(e string) bool { return strings.Contains(err.Error(), e) }) {
			continue
		}
		if err := b.store.SetReminder(r); err != nil {
			slog.Error("requeue reminder", "ts", r.TS, "err", err)
		}
	}
}

func (b *Bot) onEdit(channel, ts, text string) {
	t := b.get(channel, ts)
	if t == nil || t.Text == text {
		return
	}
	before := t.Checklist()
	t.SetText(text)
	b.save(t)
	if !slices.Equal(before, t.Checklist()) {
		b.updateCard(t)
	}
	b.refreshBoard(t.Channel)
}

func (b *Bot) onDelete(channel, ts string) {
	t := b.get(channel, ts)
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
	b.refreshBoard(t.Channel)
}

func (b *Bot) onReaction(user, reaction string, item slackevents.Item, added bool) {
	if item.Type != "message" || !b.serves(item.Channel) || user == b.botUserID {
		return
	}
	a, ok := b.cfg.Action(reaction)
	if !ok {
		return
	}
	t := b.get(item.Channel, item.Timestamp)
	if t == nil && added {
		t = b.adopt(item.Channel, item.Timestamp)
	}
	if t == nil {
		return
	}
	var eff task.Effect
	if b.cfg.StatusClaims {
		eff = t.ReactOpen(a, user, added, !added && t.IsOwner(user) && b.holding(t, user, reaction), b.now())
	} else {
		eff = t.React(a, user, added, b.now())
	}
	switch eff {
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
	b.refreshBoard(t.Channel)
}

// holding reports whether user still has a claim or status reaction on the task
// message other than removed, which the removal event already rules out even if
// reactions.get still lists it. When Slack can't be asked it reports true: a user switching status
// adds the new reaction before removing the old one, and dropping them then
// would be wrong far more often than keeping a leaver.
func (b *Bot) holding(t *task.Task, user, removed string) bool {
	r, err := b.api.GetReactions(slack.ItemRef{Channel: t.Channel, Timestamp: t.TS}, slack.GetReactionsParameters{Full: true})
	if err != nil {
		slog.Warn("get reactions", "ts", t.TS, "err", err)
		return true
	}
	removed, _, _ = strings.Cut(removed, "::")
	for _, re := range r.Reactions {
		if base, _, _ := strings.Cut(re.Name, "::"); base == removed {
			continue
		}
		if _, ok := b.cfg.Action(re.Name); ok && slices.Contains(re.Users, user) {
			return true
		}
	}
	return false
}

// adopt turns a top-level message that predates the bot (or was missed while it
// was offline) into a task when someone reacts to it.
//
// Messages older than done_retain_days are never adopted: every swept task is
// that old, and adopting one would reopen finished work under a second card.
func (b *Bot) adopt(channel, ts string) *task.Task {
	if r := b.cfg.DoneRetain(); r > 0 {
		if sec, err := strconv.ParseFloat(ts, 64); err == nil && b.now().Sub(time.Unix(int64(sec), 0)) >= r {
			return nil
		}
	}
	h, err := b.api.GetConversationHistory(&slack.GetConversationHistoryParameters{
		ChannelID: channel, Latest: ts, Oldest: ts, Inclusive: true, Limit: 1})
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
	return b.create(channel, ts, b.reporter(channel, m.User, m.Username, m.BotID), m.Text)
}

func (b *Bot) remindStale() {
	for _, c := range b.channels() {
		b.remindStaleIn(c)
	}
}

func (b *Bot) remindStaleIn(channel string) {
	open, err := b.store.Open(channel)
	if err != nil {
		slog.Error("list open", "channel", channel, "err", err)
		return
	}
	now := b.now()
	for i := range open {
		t := &open[i]
		if !t.Stale(now, b.cfg.StaleAfter()) {
			continue
		}
		handBack := fmt.Sprintf("remove your :%s:", b.emoji[task.Claim])
		if b.cfg.StatusClaims {
			handBack = "remove your reactions"
		}
		b.say(t, fmt.Sprintf("%s: no update here for %s. Still on it? Post a status here, or %s to hand it back.",
			task.Mentions(t.Owners), hours(now.Sub(t.LastActivity)), handBack))
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
	for _, c := range b.channels() {
		n, err := b.store.DeleteDone(c, b.now().Add(-b.cfg.DoneRetain()))
		if err != nil {
			slog.Error("sweep done", "channel", c, "err", err)
			continue
		}
		if n > 0 {
			slog.Info("swept done tasks", "channel", c, "count", n)
		}
	}
}

func (b *Bot) updateCard(t *task.Task) {
	// If Slack rejects the checklist blocks, fall back to the plain-text card, so
	// a checklist can never cost a task its card. Other errors (rate limits,
	// timeouts) must not strip the checkboxes or post a second card.
	if err := b.putCard(t, cardBlocks(t, b.emoji, b.cfg.StatusClaims)); err != nil && strings.HasPrefix(err.Error(), "invalid_blocks") {
		b.putCard(t, nil)
	}
}

// putCard edits or posts the card. A nil blocks renders text only and clears
// blocks on an edit.
func (b *Bot) putCard(t *task.Task, blocks []slack.Block) error {
	opts := []slack.MsgOption{slack.MsgOptionText(task.Card(*t, b.emoji, b.cfg.StatusClaims), false), slack.MsgOptionBlocks(blocks...)}
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
func cardBlocks(t *task.Task, emoji map[task.Action]string, statusClaims bool) []slack.Block {
	blocks := []slack.Block{slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, task.Card(*t, emoji, statusClaims), false, false), nil, nil)}
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

// refreshBoards brings every channel's board up to date, as on start.
func (b *Bot) refreshBoards() {
	for _, c := range b.channels() {
		b.refreshBoard(c)
	}
}

func (b *Bot) refreshBoard(channel string) {
	open, err := b.store.Open(channel)
	if err != nil {
		slog.Error("list open", "channel", channel, "err", err)
		return
	}
	text := slack.MsgOptionText(task.Board(open, b.emoji, b.cfg.BoardMaxTasks, b.now()), false)
	ts, err := b.store.KV(boardKey(channel))
	if err != nil {
		slog.Error("board ts", "channel", channel, "err", err)
		return
	}
	if ts != "" {
		if _, _, _, err := b.api.UpdateMessage(channel, ts, text, slack.MsgOptionDisableLinkUnfurl()); err == nil {
			return
		} else if err.Error() != "message_not_found" {
			slog.Warn("update board", "channel", channel, "err", err)
			return
		}
	}
	// Post a placeholder and edit the content in: a new message would notify every
	// owner and @here in the excerpts, an edit notifies nobody.
	_, ts, err = b.api.PostMessage(channel, slack.MsgOptionText("I take it: loading board…", false))
	if err != nil {
		slog.Warn("post board", "channel", channel, "err", err)
		return
	}
	if _, _, _, err := b.api.UpdateMessage(channel, ts, text, slack.MsgOptionDisableLinkUnfurl()); err != nil {
		slog.Warn("fill board", "channel", channel, "err", err)
		b.api.DeleteMessage(channel, ts) // retried on the next change
		return
	}
	if err := b.api.AddPin(channel, slack.ItemRef{Channel: channel, Timestamp: ts}); err != nil {
		slog.Warn("pin board", "channel", channel, "err", err)
	}
	if err := b.store.SetKV(boardKey(channel), ts); err != nil {
		slog.Error("save board ts", "channel", channel, "err", err)
	}
}

func (b *Bot) say(t *task.Task, text string) {
	if _, _, err := b.api.PostMessage(t.Channel, slack.MsgOptionText(text, false), slack.MsgOptionTS(t.TS)); err != nil {
		slog.Warn("post thread", "ts", t.TS, "err", err)
	}
}

func (b *Bot) get(channel, ts string) *task.Task {
	t, err := b.store.Get(channel, ts)
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

// reporter names who to ask for details. A message from a bot or integration
// reports as the channel's bot contact (bot_contacts, else bot_contact) when there is one, since the bot can't
// answer a question and a plain-text name pings nobody.
func (b *Bot) reporter(channel, user, username, botID string) string {
	switch {
	case botID != "" && b.cfg.BotContactFor(channel) != "":
		return b.cfg.BotContactFor(channel)
	case user != "":
		return user
	case username != "":
		return username
	case botID != "":
		return "bot " + botID
	}
	return "unknown"
}

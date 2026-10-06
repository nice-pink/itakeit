// Package config loads the itakeit YAML config and maps reactions to actions.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/nice-pink/itakeit/pkg/task"
	"gopkg.in/yaml.v3"
)

type Config struct {
	// Channels lists the channel IDs the bot serves, all with the settings below.
	// The older single `channel` key is still read and becomes a one-item list.
	Channels []string `yaml:"channels"`
	// LegacyChannel is folded into Channels by Parse, except under auto_channels,
	// where it is kept but ignored: a file shared with an itakeit-agent from
	// before it read channels and auto_channels still sets `channel` for it.
	LegacyChannel string `yaml:"channel"`
	// AutoChannels serves every channel the bot is a member of instead of a list.
	AutoChannels    bool   `yaml:"auto_channels"`
	DBPath          string `yaml:"db_path"`
	DatabaseURL     string `yaml:"database_url"`
	StaleAfterHours int    `yaml:"stale_after_hours"`
	BoardMaxTasks   int    `yaml:"board_max_tasks"`
	DoneRetainDays  int    `yaml:"done_retain_days"`
	StatusClaims    bool   `yaml:"status_claims"`
	// BotContact is a Slack user ID that becomes the reporter of tasks posted by
	// bots and integrations, so needs_info pings a person instead of the bot.
	BotContact string `yaml:"bot_contact"`
	// BotContacts overrides BotContact per channel ID. Keys need not be listed in
	// Channels, so it works under auto_channels too.
	BotContacts map[string]string        `yaml:"bot_contacts"`
	Emoji       map[task.Action][]string `yaml:"emoji"`

	byEmoji  map[string]task.Action
	channels map[string]bool
}

// channelID matches public (C) and private (G, older) channel IDs. A channel name
// such as #itakeit would otherwise load fine and be ignored forever.
var channelID = regexp.MustCompile(`^[CG][A-Z0-9]+$`)

var userID = regexp.MustCompile(`^[UW][A-Z0-9]{6,}$`)

// Defaults applied for any field left empty in the file.
var Defaults = Config{
	DBPath:          "itakeit.db",
	StaleAfterHours: 48,
	BoardMaxTasks:   50,
	DoneRetainDays:  30,
	Emoji: map[task.Action][]string{
		task.Claim:         {"raising_hand"},
		task.Investigating: {"eyes"},
		task.InProgress:    {"construction"},
		task.NeedsInfo:     {"question"},
		task.Blocked:       {"no_entry"},
		task.Done:          {"white_check_mark"},
	},
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	c := Config{}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.DBPath == "" {
		c.DBPath = Defaults.DBPath
	}
	if c.StaleAfterHours == 0 {
		c.StaleAfterHours = Defaults.StaleAfterHours
	}
	if c.BoardMaxTasks == 0 {
		c.BoardMaxTasks = Defaults.BoardMaxTasks
	}
	if c.DoneRetainDays == 0 {
		c.DoneRetainDays = Defaults.DoneRetainDays
	}
	if len(c.Emoji) == 0 {
		c.Emoji = Defaults.Emoji
	}
	return &c, c.index()
}

func (c *Config) index() error {
	if c.LegacyChannel != "" && !c.AutoChannels {
		if len(c.Channels) > 0 {
			return errors.New("config: set channels or channel, not both")
		}
		c.Channels, c.LegacyChannel = []string{c.LegacyChannel}, ""
	}
	if c.AutoChannels {
		if len(c.Channels) > 0 {
			return errors.New("config: set channels or auto_channels, not both")
		}
	} else if len(c.Channels) == 0 {
		return errors.New("config: channels is required (channel IDs like C0123456789), or set auto_channels: true")
	}
	c.channels = map[string]bool{}
	for i, ch := range c.Channels {
		ch = strings.TrimSpace(ch)
		if !IsChannelID(ch) {
			return fmt.Errorf("config: %q is not a channel ID (like C0123456789, from channel details -> About)", ch)
		}
		c.Channels[i] = ch
		if c.channels[ch] {
			return fmt.Errorf("config: channel %q listed twice", ch)
		}
		c.channels[ch] = true
	}
	if c.StaleAfterHours < 0 || c.BoardMaxTasks < 0 {
		return errors.New("config: stale_after_hours and board_max_tasks must be positive")
	}
	if c.DoneRetainDays < -1 {
		return errors.New("config: done_retain_days must be positive, or -1 to keep done tasks forever")
	}
	c.BotContact = strings.TrimSpace(c.BotContact)
	if c.BotContact != "" && !userID.MatchString(c.BotContact) {
		return fmt.Errorf("config: bot_contact %q is not a Slack user ID (like U0123456789, from the profile's more menu -> Copy member ID)", c.BotContact)
	}
	for ch, u := range c.BotContacts {
		if !IsChannelID(ch) {
			return fmt.Errorf("config: bot_contacts key %q is not a channel ID (like C0123456789)", ch)
		}
		if !userID.MatchString(strings.TrimSpace(u)) {
			return fmt.Errorf("config: bot_contacts[%s] %q is not a Slack user ID (like U0123456789)", ch, u)
		}
		c.BotContacts[ch] = strings.TrimSpace(u)
	}
	if len(c.Emoji[task.Claim]) == 0 {
		return errors.New("config: emoji.claim needs at least one emoji")
	}
	c.byEmoji = map[string]task.Action{}
	for a, names := range c.Emoji {
		if !task.ValidAction(a) {
			return fmt.Errorf("config: unknown action %q", a)
		}
		for _, n := range names {
			n = strings.Trim(n, ": ")
			if prev, dup := c.byEmoji[n]; dup {
				return fmt.Errorf("config: emoji %q mapped to both %q and %q", n, prev, a)
			}
			c.byEmoji[n] = a
		}
	}
	return nil
}

// Serves reports whether channel is one of the configured channels. It is
// always false under auto_channels, where the bot tracks membership itself.
func (c *Config) Serves(channel string) bool { return c.channels[channel] }

// BotContactFor returns the user asked for details on tasks bots post in channel:
// its bot_contacts entry, else bot_contact, else "" (no contact).
func (c *Config) BotContactFor(channel string) string {
	if u := c.BotContacts[channel]; u != "" {
		return u
	}
	return c.BotContact
}

// IsChannelID reports whether id is a public or private channel ID, which rules
// out direct messages (D...) and channel names.
func IsChannelID(id string) bool { return channelID.MatchString(id) }

// Action resolves a reaction name. Skin tone variants ("raising_hand::skin-tone-3")
// resolve to their base emoji.
func (c *Config) Action(reaction string) (task.Action, bool) {
	base, _, _ := strings.Cut(reaction, "::")
	a, ok := c.byEmoji[base]
	return a, ok
}

// Display returns the emoji shown for each action: the first one listed.
func (c *Config) Display() map[task.Action]string {
	d := map[task.Action]string{}
	for a, names := range c.Emoji {
		if len(names) > 0 {
			d[a] = strings.Trim(names[0], ": ")
		}
	}
	return d
}

func (c *Config) StaleAfter() time.Duration {
	return time.Duration(c.StaleAfterHours) * time.Hour
}

// DoneRetain is how long a done task stays in the database. Zero means forever
// (done_retain_days: -1).
func (c *Config) DoneRetain() time.Duration {
	return time.Duration(max(c.DoneRetainDays, 0)) * 24 * time.Hour
}

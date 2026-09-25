// Package config loads the itakeit YAML config and maps reactions to actions.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nice-pink/itakeit/pkg/task"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Channel         string                   `yaml:"channel"`
	DBPath          string                   `yaml:"db_path"`
	StaleAfterHours int                      `yaml:"stale_after_hours"`
	BoardMaxTasks   int                      `yaml:"board_max_tasks"`
	DoneRetainDays  int                      `yaml:"done_retain_days"`
	Emoji           map[task.Action][]string `yaml:"emoji"`

	byEmoji map[string]task.Action
}

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
	// 0 is a valid value (keep forever), so only an absent key gets the default.
	var set struct {
		DoneRetainDays *int `yaml:"done_retain_days"`
	}
	yaml.Unmarshal(raw, &set)
	if set.DoneRetainDays == nil {
		c.DoneRetainDays = Defaults.DoneRetainDays
	}
	if len(c.Emoji) == 0 {
		c.Emoji = Defaults.Emoji
	}
	return &c, c.index()
}

func (c *Config) index() error {
	if c.Channel == "" {
		return errors.New("config: channel is required (a channel ID like C0123456789)")
	}
	if c.StaleAfterHours < 0 || c.BoardMaxTasks < 0 || c.DoneRetainDays < 0 {
		return errors.New("config: stale_after_hours, board_max_tasks and done_retain_days must not be negative")
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

// DoneRetain is how long a done task stays in the database. Zero keeps them forever.
func (c *Config) DoneRetain() time.Duration {
	return time.Duration(c.DoneRetainDays) * 24 * time.Hour
}

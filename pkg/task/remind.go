package task

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// RemindDefaultHour is the local hour a reminder fires at when the request
// names a day but no time.
const RemindDefaultHour = 9

const remindMaxAhead = 366 * 24 * time.Hour

var (
	remindIn = regexp.MustCompile(`^in (\d{1,3}) ?(m|min|mins|minutes?|h|hr|hrs|hours?|d|days?|w|weeks?)$`)
	remindAt = regexp.MustCompile(`^(?:(today|tomorrow|mon(?:day)?|tue(?:s(?:day)?)?|wed(?:nesday)?|thu(?:rs(?:day)?)?|fri(?:day)?|sat(?:urday)?|sun(?:day)?)(?:\s+|$))?(?:at\s+)?(?:(\d{1,2})(?::(\d{2}))?\s*(am|pm)?)?$`)
	weekdays = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
		"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}
)

const remindPrefix = "remind me"

// IsRemind reports whether a reply is a reminder request, parsable or not.
func IsRemind(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	rest, ok := strings.CutPrefix(s, remindPrefix)
	if !ok || rest == "" {
		return ok
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// ParseRemind reads a thread reply of the form "remind me <when>" and returns
// the due time. The whole reply must match; IsRemind replies that do not parse
// get a usage hint from the bot rather than being treated as ordinary replies.
// When is one of:
//
//	in 30m | in 3 hours | in 2 days | in 1w
//	tomorrow | monday | today at 15:00 | tomorrow at 3pm | at 15:00
//
// A time needs minutes or am/pm ("at 3" is rejected as ambiguous).
//
// A day without a time means RemindDefaultHour in loc. A bare time means its
// next occurrence.
func ParseRemind(text string, now time.Time, loc *time.Location) (time.Time, bool) {
	if !IsRemind(text) {
		return time.Time{}, false
	}
	s := strings.ToLower(strings.TrimSpace(text))
	rest := strings.TrimRight(strings.TrimPrefix(s, remindPrefix), ".! ")
	rest = strings.Join(strings.Fields(rest), " ")
	due, ok := parseWhen(rest, now.In(loc))
	if !ok || !due.After(now) || due.Sub(now) > remindMaxAhead {
		return time.Time{}, false
	}
	return due, true
}

func parseWhen(s string, now time.Time) (time.Time, bool) {
	if m := remindIn.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2][0] {
		case 'm':
			return now.Add(time.Duration(n) * time.Minute), n > 0
		case 'h':
			return now.Add(time.Duration(n) * time.Hour), n > 0
		case 'd':
			return now.AddDate(0, 0, n), n > 0
		}
		return now.AddDate(0, 0, 7*n), n > 0
	}
	m := remindAt.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	day, hh, mm, ampm := m[1], m[2], m[3], m[4]
	if day == "" && hh == "" {
		return time.Time{}, false
	}
	hour, min := RemindDefaultHour, 0
	if hh != "" {
		if mm == "" && ampm == "" {
			return time.Time{}, false // "at 3" and "monday 3" are ambiguous
		}
		hour, _ = strconv.Atoi(hh)
		min, _ = strconv.Atoi(mm)
		if min > 59 {
			return time.Time{}, false
		}
		if ampm != "" {
			if hour < 1 || hour > 12 {
				return time.Time{}, false
			}
			hour %= 12
			if ampm == "pm" {
				hour += 12
			}
		} else if hour > 23 {
			return time.Time{}, false
		}
	} else if day == "today" {
		return time.Time{}, false
	}
	days := 0
	switch {
	case day == "tomorrow":
		days = 1
	case day != "" && day != "today":
		days = (int(weekdays[day[:3]]) - int(now.Weekday()) + 7) % 7
		if days == 0 {
			days = 7
		}
	}
	due := time.Date(now.Year(), now.Month(), now.Day()+days, hour, min, 0, 0, now.Location())
	if day == "" && !due.After(now) {
		due = time.Date(now.Year(), now.Month(), now.Day()+1, hour, min, 0, 0, now.Location())
	}
	return due, true
}

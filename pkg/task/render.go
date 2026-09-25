package task

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var labels = map[Action]string{
	Investigating: "investigating",
	InProgress:    "in progress",
	NeedsInfo:     "needs info",
	Blocked:       "blocked",
	Done:          "done",
}

// Label is the human status, derived from owners when no explicit status is set.
func (t Task) Label(emoji map[Action]string) string {
	switch {
	case t.Status != "":
		return icon(emoji, t.Status) + labels[t.Status]
	case len(t.Owners) > 0:
		return icon(emoji, Claim) + "claimed"
	default:
		return ":white_circle: unclaimed"
	}
}

// Card is the status message the bot keeps in each task's thread. statusClaims
// is config status_claims: a status reaction makes the reactor an owner.
func Card(t Task, emoji map[Action]string, statusClaims bool) string {
	owners := "nobody yet"
	if len(t.Owners) > 0 {
		owners = Mentions(t.Owners)
	}
	legend := []string{icon(emoji, Claim) + "take it"}
	for _, a := range statusActions {
		if e, ok := emoji[a]; ok {
			legend = append(legend, ":"+e+": "+labels[a])
		}
	}
	checklist := ""
	if done, total := t.Progress(); total > 0 {
		checklist = fmt.Sprintf("*Checklist:* %d/%d\n", done, total)
	}
	rule := "Status reactions count from owners only."
	if statusClaims {
		rule = "Any of these makes you an owner."
	}
	return fmt.Sprintf("*Status:* %s\n*Owners:* %s\n%s_React on the message above: %s. %s_",
		t.Label(emoji), owners, checklist, strings.Join(legend, " · "), rule)
}

// Board is the pinned overview of all open tasks, oldest first.
func Board(tasks []Task, emoji map[Action]string, max int, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*I take it: %d open* (updated <!date^%d^{date_short_pretty} {time}|%s>)\n",
		len(tasks), now.Unix(), now.UTC().Format(time.RFC3339))
	if len(tasks) == 0 {
		b.WriteString("Nothing open. :tada:")
		return b.String()
	}
	for i, t := range tasks {
		if max > 0 && i == max {
			fmt.Fprintf(&b, "…and %d more", len(tasks)-max)
			break
		}
		who := "unclaimed"
		if len(t.Owners) > 0 {
			who = Mentions(t.Owners)
		}
		if done, total := t.Progress(); total > 0 {
			who += fmt.Sprintf(" · %d/%d", done, total)
		}
		fmt.Fprintf(&b, "• %s  %s · %s · <%s|open>\n", t.Label(emoji), Excerpt(t.Text, 80), who, t.Permalink)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Mention renders a user ID as a mention; bot names pass through as plain text.
func Mention(id string) string {
	if userID.MatchString(id) {
		return "<@" + id + ">"
	}
	return id
}

var userID = regexp.MustCompile(`^[UW][A-Z0-9]{6,}$`)

func Mentions(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = Mention(id)
	}
	return strings.Join(out, ", ")
}

// Excerpt returns the first line of Slack mrkdwn cut to n runes without splitting
// a <...> link/mention or an &entity; (Slack escapes <, > and & as entities).
func Excerpt(text string, n int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	r := []rune(line)
	if len(r) <= n {
		return line
	}
	cut := n
	if i := lastIndex(r[:cut], '<'); i >= 0 && lastIndex(r[i:cut], '>') < 0 {
		cut = i
	}
	if i := lastIndex(r[:cut], '&'); i >= 0 && lastIndex(r[i:cut], ';') < 0 {
		cut = i
	}
	return strings.TrimSpace(string(r[:cut])) + "…"
}

func lastIndex(r []rune, c rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == c {
			return i
		}
	}
	return -1
}

func icon(emoji map[Action]string, a Action) string {
	if e, ok := emoji[a]; ok {
		return ":" + e + ": "
	}
	return ""
}

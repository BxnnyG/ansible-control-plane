// Package schedules implements cron schedules for templates (RUN-6) and the
// public template webhooks (ADR-0016). It owns cron parsing, next-run
// computation, the scheduler loop and the webhook signature handling; the run
// itself is created by the runs package.
package schedules

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones must not depend on the runtime image
)

// ErrInvalid marks validation errors (cron expression, timezone, policy).
var ErrInvalid = errors.New("invalid request")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Cron is a parsed five-field cron expression (minute hour day-of-month month
// day-of-week). Like Vixie cron, a day matches when either restricted day
// field matches; if one of them is "*" only the other counts.
type Cron struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

var descriptors = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

var dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseCron validates and parses a cron expression or one of the @hourly,
// @daily, @weekly, @monthly, @yearly descriptors.
func ParseCron(expr string) (*Cron, error) {
	expr = strings.TrimSpace(expr)
	if d, ok := descriptors[strings.ToLower(expr)]; ok {
		expr = d
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, invalid("cron expression needs 5 fields (minute hour day-of-month month day-of-week)")
	}
	c := &Cron{}
	var err error
	if c.minute, _, err = parseField(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if c.hour, _, err = parseField(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if c.dom, c.domStar, err = parseField(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if c.month, _, err = parseField(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if c.dow, c.dowStar, err = parseField(f[4], 0, 7, dowNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday as well
		c.dow = c.dow&^(1<<7) | 1
	}
	return c, nil
}

// parseField parses one field into a bit set; star reports a leading "*".
func parseField(s string, lo, hi int, names map[string]int) (set uint64, star bool, err error) {
	num := func(v string) (int, error) {
		if n, ok := names[strings.ToLower(v)]; ok {
			return n, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi || strings.HasPrefix(v, "+") || strings.HasPrefix(v, "-") {
			return 0, invalid("value %q out of range %d-%d", v, lo, hi)
		}
		return n, nil
	}
	for _, part := range strings.Split(s, ",") {
		rng, stepS, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			if step, err = strconv.Atoi(stepS); err != nil || step < 1 {
				return 0, false, invalid("invalid step %q", stepS)
			}
		}
		var a, b int
		switch {
		case rng == "*":
			a, b = lo, hi
			if !hasStep && len(strings.Split(s, ",")) == 1 {
				star = true
			}
			if hi == 7 {
				b = 6 // day of week: * means 0-6
			}
		case strings.Contains(rng, "-"):
			from, to, _ := strings.Cut(rng, "-")
			if a, err = num(from); err != nil {
				return 0, false, err
			}
			if b, err = num(to); err != nil {
				return 0, false, err
			}
			if a > b {
				return 0, false, invalid("range %q is reversed", rng)
			}
		default:
			if a, err = num(rng); err != nil {
				return 0, false, err
			}
			b = a
			if hasStep {
				b = hi
			}
		}
		for v := a; v <= b; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, star, nil
}

func has(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

func (c *Cron) dayMatches(t time.Time) bool {
	if !has(c.month, int(t.Month())) {
		return false
	}
	dom, dow := has(c.dom, t.Day()), has(c.dow, int(t.Weekday()))
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dow
	case c.dowStar:
		return dom
	}
	return dom || dow
}

// Next returns the first matching instant strictly after t, evaluated in t's
// location. Wall-clock times that do not exist (spring-forward gap) are
// skipped; a wall-clock time that occurs twice (fall-back) fires once, at its
// first occurrence. The zero time is returned when nothing matches within
// five years (for example 31 February).
func (c *Cron) Next(t time.Time) time.Time {
	loc := t.Location()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	end := day.AddDate(5, 0, 0)
	for ; day.Before(end); day = time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc) {
		if !c.dayMatches(day) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !has(c.hour, h) {
				continue
			}
			for m := 0; m < 60; m++ {
				if !has(c.minute, m) {
					continue
				}
				cand := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc)
				if cand.Hour() != h || cand.Minute() != m { // nonexistent local time
					continue
				}
				if cand.After(t) {
					return cand
				}
			}
		}
	}
	return time.Time{}
}

// LoadLocation resolves an IANA timezone name. "Local" is rejected so
// schedules do not depend on the container's timezone.
func LoadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "UTC"
	}
	if strings.EqualFold(name, "local") {
		return nil, invalid("timezone must be an IANA name such as Europe/Berlin")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, invalid("unknown timezone %q", name)
	}
	return loc, nil
}

// NextRuns returns up to n upcoming run times of expr in the timezone, after
// the given instant.
func NextRuns(expr, tz string, after time.Time, n int) ([]time.Time, error) {
	c, err := ParseCron(expr)
	if err != nil {
		return nil, err
	}
	loc, err := LoadLocation(tz)
	if err != nil {
		return nil, err
	}
	var out []time.Time
	t := after.In(loc)
	for len(out) < n {
		t = c.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out, nil
}

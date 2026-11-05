package schedules

import (
	"errors"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestParseCronInvalid(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *",
		"* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *", "-1 * * * *", "@reboot", "1,,2 * * * *"} {
		if _, err := ParseCron(expr); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseCron(%q) err = %v, want ErrInvalid", expr, err)
		}
	}
}

func TestNext(t *testing.T) {
	utc := time.UTC
	tests := []struct {
		name, expr string
		after      time.Time
		want       time.Time
	}{
		{"every minute", "* * * * *", time.Date(2026, 1, 1, 10, 0, 30, 0, utc), time.Date(2026, 1, 1, 10, 1, 0, 0, utc)},
		{"strictly after", "0 10 * * *", time.Date(2026, 1, 1, 10, 0, 0, 0, utc), time.Date(2026, 1, 2, 10, 0, 0, 0, utc)},
		{"step", "*/15 * * * *", time.Date(2026, 1, 1, 10, 16, 0, 0, utc), time.Date(2026, 1, 1, 10, 30, 0, 0, utc)},
		{"list and range", "0 8-10/2,20 * * *", time.Date(2026, 1, 1, 9, 0, 0, 0, utc), time.Date(2026, 1, 1, 10, 0, 0, 0, utc)},
		{"weekday names", "0 6 * * mon-fri", time.Date(2026, 1, 3, 0, 0, 0, 0, utc), time.Date(2026, 1, 5, 6, 0, 0, 0, utc)}, // Sat -> Mon
		{"sunday as 7", "0 6 * * 7", time.Date(2026, 1, 1, 0, 0, 0, 0, utc), time.Date(2026, 1, 4, 6, 0, 0, 0, utc)},
		{"month name", "0 0 1 mar *", time.Date(2026, 1, 1, 0, 0, 0, 0, utc), time.Date(2026, 3, 1, 0, 0, 0, 0, utc)},
		{"dom or dow", "0 0 13 * fri", time.Date(2026, 1, 1, 0, 0, 0, 0, utc), time.Date(2026, 1, 2, 0, 0, 0, 0, utc)}, // Friday the 2nd
		{"descriptor", "@daily", time.Date(2026, 1, 1, 10, 0, 0, 0, utc), time.Date(2026, 1, 2, 0, 0, 0, 0, utc)},
		{"leap day", "0 0 29 2 *", time.Date(2026, 1, 1, 0, 0, 0, 0, utc), time.Date(2028, 2, 29, 0, 0, 0, 0, utc)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCron(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Next(tc.after); !got.Equal(tc.want) {
				t.Errorf("Next = %v, want %v", got, tc.want)
			}
		})
	}
	if c, _ := ParseCron("0 0 31 2 *"); !c.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, utc)).IsZero() {
		t.Error("31 February must never match")
	}
}

func TestNextDST(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, berlin) }
	tests := []struct {
		name, expr string
		after      time.Time
		want       time.Time
		wantUTC    string // checks the instant, not only the wall clock
	}{
		// 2026-03-29: 02:00 CET jumps to 03:00 CEST, 02:30 does not exist.
		{"spring gap is skipped", "30 2 * * *", at(2026, 3, 28, 12, 0), at(2026, 3, 30, 2, 30), "2026-03-30T00:30:00Z"},
		{"after the gap", "0 3 * * *", at(2026, 3, 29, 0, 0), at(2026, 3, 29, 3, 0), "2026-03-29T01:00:00Z"},
		{"hourly across the gap", "0 * * * *", at(2026, 3, 29, 1, 30), at(2026, 3, 29, 3, 0), "2026-03-29T01:00:00Z"},
		// 2026-10-25: 03:00 CEST falls back to 02:00 CET, 02:30 occurs twice and fires once.
		{"autumn overlap first occurrence", "30 2 * * *", at(2026, 10, 24, 12, 0), at(2026, 10, 25, 2, 30), "2026-10-25T00:30:00Z"},
		{"autumn overlap once", "30 2 * * *", time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).In(berlin), at(2026, 10, 26, 2, 30), "2026-10-26T01:30:00Z"},
		{"daily 12:00 keeps wall clock", "0 12 * * *", at(2026, 10, 24, 12, 0), at(2026, 10, 25, 12, 0), "2026-10-25T11:00:00Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCron(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			got := c.Next(tc.after)
			if !got.Equal(tc.want) || got.UTC().Format(time.RFC3339) != tc.wantUTC {
				t.Errorf("Next = %v (%s), want %v (%s)", got, got.UTC().Format(time.RFC3339), tc.want, tc.wantUTC)
			}
		})
	}
}

func TestNextRunsAndTimezone(t *testing.T) {
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := NextRuns("0 9 * * *", "Europe/Berlin", after, 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("NextRuns: %v %v", got, err)
	}
	if got[0].UTC().Format(time.RFC3339) != "2026-01-01T08:00:00Z" || got[1].Sub(got[0]) != 24*time.Hour {
		t.Errorf("runs = %v", got)
	}
	if _, err := NextRuns("0 9 * * *", "Mars/Olympus", after, 1); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown tz err = %v", err)
	}
	if _, err := NextRuns("0 9 * * *", "Local", after, 1); !errors.Is(err, ErrInvalid) {
		t.Errorf("Local tz err = %v", err)
	}
	if l, err := LoadLocation(""); err != nil || l != time.UTC {
		t.Errorf("empty tz = %v %v", l, err)
	}
}

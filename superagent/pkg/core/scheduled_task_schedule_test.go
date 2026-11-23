package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestScheduledTaskRules(t *testing.T) {
	cases := []struct {
		name, rule, anchor, after, expected string
	}{
		{"once retains its absolute past time", `{"type":"once","runAt":"2026-11-25T09:00:00.123+08:00"}`,
			"2026-11-26T01:00:00Z", "2026-11-26T01:00:00Z", "2026-11-25T01:00:00.123Z"},
		{"interval stays on anchor phase", `{"type":"interval","every":"30m"}`,
			"2026-11-25T09:13:14Z", "2026-11-27T09:14:03Z", "2026-11-27T09:43:14Z"},
		{"interval beyond duration subtraction range", `{"type":"interval","every":"100ms"}`,
			"1700-01-01T09:00:00.033Z", "2026-11-25T09:00:00.930Z", "2026-11-25T09:00:00.933Z"},
		{"daily same day with unordered duplicates", `{"type":"daily","times":["18:00","09:00","18:00"],"timezone":"Asia/Shanghai"}`,
			"2026-11-25T01:00:00Z", "2026-11-25T01:00:00Z", "2026-11-25T10:00:00Z"},
		{"every other week stays on the Monday-based anchor", `{"type":"weekly","every":2,"weekdays":["mon","fri"],"times":["09:00"],"timezone":"UTC"}`,
			"2026-11-25T10:00:00Z", "2026-11-28T09:00:00Z", "2026-12-07T09:00:00Z"},
		{"monthly end of leap February", `{"type":"monthly","days":[1,-1],"times":["09:00"],"timezone":"UTC"}`,
			"2028-02-01T09:00:00Z", "2028-02-01T09:00:00Z", "2028-02-29T09:00:00Z"},
		{"monthly short and inactive months skipped", `{"type":"monthly","every":2,"days":[31],"times":["09:00"],"timezone":"UTC"}`,
			"2026-02-27T09:00:00Z", "2026-02-27T09:00:00Z", "2026-08-31T09:00:00Z"},
		{"daily DST spring gap", `{"type":"daily","times":["02:30"],"timezone":"America/New_York"}`,
			"2026-03-01T00:00:00Z", "2026-03-07T08:00:00Z", "2026-03-09T06:30:00Z"},
		{"daily DST repeated minute takes first", `{"type":"daily","times":["01:30"],"timezone":"America/New_York"}`,
			"2026-10-31T00:00:00Z", "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z"},
		{"daily DST second occurrence skipped", `{"type":"daily","times":["01:30"],"timezone":"America/New_York"}`,
			"2026-10-31T00:00:00Z", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"},
		{"daily whole date skipped in Samoa", `{"type":"daily","times":["09:00"],"timezone":"Pacific/Apia"}`,
			"2011-12-25T00:00:00Z", "2011-12-29T21:00:00Z", "2011-12-30T19:00:00Z"},
		{"cron uses DOM or DOW when both restricted", `{"type":"cron","expression":"0 9 1 * 1","timezone":"UTC"}`,
			"2026-11-25T09:00:00Z", "2026-11-30T09:00:00Z", "2026-12-01T09:00:00Z"},
		{"cron stepped wildcard restricts day of month", `{"type":"cron","expression":"0 9 */2 * 1","timezone":"UTC"}`,
			"2026-11-25T09:00:00Z", "2026-11-25T09:00:00Z", "2026-11-27T09:00:00Z"},
		{"cron skips DST gap", `{"type":"cron","expression":"30 2 * * *","timezone":"America/New_York"}`,
			"2026-03-01T00:00:00Z", "2026-03-07T08:00:00Z", "2026-03-09T06:30:00Z"},
		{"cron skips second occurrence after fractional-hour rollback", `{"type":"cron","expression":"45 1 * * *","timezone":"Australia/Lord_Howe"}`,
			"2026-04-01T00:00:00Z", "2026-04-04T15:00:00Z", "2026-04-05T15:15:00Z"},
		{"cron spans the century leap-year exception", `{"type":"cron","expression":"0 9 29 2 *","timezone":"UTC"}`,
			"2096-02-29T09:00:00Z", "2096-02-29T09:00:00Z", "2104-02-29T09:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var schedule ScheduledTaskSchedule
			if err := json.Unmarshal([]byte(c.rule), &schedule); err != nil {
				t.Fatal(err)
			}
			anchor := parseScheduleTestTime(t, c.anchor)
			after := parseScheduleTestTime(t, c.after)
			expected := parseScheduleTestTime(t, c.expected)
			result, err := schedule.next(anchor, after)
			if err != nil || !result.Equal(expected) || result.Location() != time.UTC {
				t.Fatalf("next = %v, %v, want %v in UTC", result, err, expected)
			}
			encoded, err := json.Marshal(schedule)
			if err != nil || json.Unmarshal(encoded, &schedule) != nil {
				t.Fatalf("schedule failed to round-trip: %s, %v", encoded, err)
			}
		})
	}
}

func TestScheduledTaskRulesRejectInvalidInput(t *testing.T) {
	anchor := parseScheduleTestTime(t, "2026-11-25T09:00:00Z")
	for _, rule := range []string{
		`null`,
		`{"Type":"once","runAt":"2026-11-25T09:00:00Z"}`,
		`{"type":"once","runAt":"2026-11-25T09:00:00Z","times":[]}`,
		`{"type":"once","runAt":"2026-11-25T09:00:00"}`,
		`{"type":"once","runAt":"2026-11-25T09:00:00+00:60"}`,
		`{"type":"interval","every":0}`,
		`{"type":"interval","every":"0m"}`,
		`{"type":"interval","every":"10000000h"}`,
		`{"type":"daily","every":0,"times":["09:00"],"timezone":"UTC"}`,
		`{"type":"daily","every":null,"times":["09:00"],"timezone":"UTC"}`,
		`{"type":"daily","times":["9:00"],"timezone":"UTC"}`,
		`{"type":"daily","times":["09:00"],"timezone":"Local"}`,
		`{"type":"daily","times":["09:00"],"timezone":"Invalid/Location"}`,
		`{"type":"daily","every":9223372036854775807,"times":["09:00"],"timezone":"UTC"}`,
		`{"type":"weekly","weekdays":[],"times":["09:00"],"timezone":"UTC"}`,
		`{"type":"monthly","days":[0],"times":["09:00"],"timezone":"UTC"}`,
		`{"type":"monthly","days":[31],"every":12,"times":["09:00"],"timezone":"UTC"}`,
		`{"type":"cron","expression":"@daily","timezone":"UTC"}`,
		`{"type":"cron","expression":"0 9 30 2 *","timezone":"UTC"}`,
		`{"type":"cron","expression":"*/0 * * * *","timezone":"UTC"}`,
		`{"type":"cron","expression":"0 0 9 * * *","timezone":"UTC"}`,
		`{"type":"unsupported"}`,
	} {
		t.Run(rule, func(t *testing.T) {
			now := anchor
			if rule == `{"type":"monthly","days":[31],"every":12,"times":["09:00"],"timezone":"UTC"}` {
				now = parseScheduleTestTime(t, "2026-02-25T09:00:00Z")
			}
			var schedule ScheduledTaskSchedule
			if json.Unmarshal([]byte(rule), &schedule) == nil {
				if next, err := schedule.next(now, now); err == nil {
					t.Fatalf("accepted invalid rule; next = %v", next)
				}
			}
		})
	}
}

func parseScheduleTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

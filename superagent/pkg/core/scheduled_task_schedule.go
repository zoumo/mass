package core

import (
	"bytes"
	"encoding/json"
	"math/big"
	"math/bits"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/robfig/cron/v3"
)

const (
	minutesPerDay   = 24 * 60
	maxScheduleYear = 9999
)

var (
	scheduledRunAtPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)
	scheduledTimePattern  = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	scheduledCronField    = regexp.MustCompile(`^[0-9*/,\-]+$`)
	scheduledCronParser   = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	scheduledWeekdays     = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	errScheduleExhausted  = problem("invalid_argument", "schedule has no future matching time")
)

// ScheduledTaskSchedule is the schedule rule. Every is a duration string for
// interval rules and an optional positive integer for calendar rules.
type ScheduledTaskSchedule struct {
	Type       string          `json:"type"`
	RunAt      string          `json:"runAt,omitempty"`
	Every      json.RawMessage `json:"every,omitempty"`
	Times      []string        `json:"times,omitempty"`
	Timezone   string          `json:"timezone,omitempty"`
	Weekdays   []string        `json:"weekdays,omitempty"`
	Days       []int           `json:"days,omitempty"`
	Expression string          `json:"expression,omitempty"`
}

func (s *ScheduledTaskSchedule) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var kind string
	if err := json.Unmarshal(fields["type"], &kind); err != nil {
		return problem("invalid_argument", "schedule.type is required")
	}
	variant := scheduledScheduleVariant(kind)
	if variant == nil {
		return problem("invalid_argument", "unsupported schedule.type")
	}
	for key, value := range fields {
		if variant.Properties[key] == nil || bytes.Equal(value, []byte("null")) {
			return problem("invalid_argument", "invalid schedule field: "+key)
		}
	}
	for _, key := range variant.Required {
		if fields[key] == nil {
			return problem("invalid_argument", "schedule."+key+" is required")
		}
	}
	type scheduleWire ScheduledTaskSchedule
	var value scheduleWire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = ScheduledTaskSchedule(value)
	return nil
}

func (*ScheduledTaskSchedule) Schema(huma.Registry) *huma.Schema {
	var variants []*huma.Schema
	for _, kind := range []string{"once", "interval", "daily", "weekly", "monthly", "cron"} {
		variants = append(variants, scheduledScheduleVariant(kind))
	}
	return &huma.Schema{Type: huma.TypeObject, OneOf: variants}
}

func scheduledTaskScheduleSchema() map[string]any {
	// Expose the same discriminated union in MCP and OpenAPI.
	data, err := json.Marshal((&ScheduledTaskSchedule{}).Schema(nil))
	if err != nil {
		panic(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		panic(err)
	}
	return schema
}

func scheduledScheduleVariant(kind string) *huma.Schema {
	positive := float64(1)
	nonempty := 1
	properties := map[string]*huma.Schema{"type": {Type: huma.TypeString, Const: kind}}
	required := []string{"type"}
	add := func(key string, property *huma.Schema) {
		properties[key] = property
		required = append(required, key)
	}
	switch kind {
	case "once":
		add("runAt", &huma.Schema{Type: huma.TypeString, Format: "date-time"})
	case "interval":
		add("every", &huma.Schema{Type: huma.TypeString, MinLength: &nonempty, Description: "Positive Go duration, e.g. 30m or 2h."})
	case "daily", "weekly", "monthly":
		properties["every"] = &huma.Schema{Type: huma.TypeInteger, Minimum: &positive, Default: 1}
		add("timezone", &huma.Schema{Type: huma.TypeString, MinLength: &nonempty, Description: "IANA time zone, e.g. Asia/Shanghai."})
		add("times", &huma.Schema{Type: huma.TypeArray, MinItems: &nonempty, Items: &huma.Schema{Type: huma.TypeString, Pattern: scheduledTimePattern.String()}})
		if kind == "weekly" {
			weekdays := make([]any, len(scheduledWeekdays))
			for i, day := range scheduledWeekdays {
				weekdays[i] = day
			}
			add("weekdays", &huma.Schema{Type: huma.TypeArray, MinItems: &nonempty, Items: &huma.Schema{Type: huma.TypeString, Enum: weekdays}})
		}
		if kind == "monthly" {
			minDay, maxDay, zero := float64(-31), float64(31), float64(0)
			add("days", &huma.Schema{Type: huma.TypeArray, MinItems: &nonempty, Items: &huma.Schema{
				Type: huma.TypeInteger, Minimum: &minDay, Maximum: &maxDay, Not: &huma.Schema{Const: zero},
				Description: "Positive days count from the start of the month. Negative days count from the end.",
			}})
		}
	case "cron":
		add("expression", &huma.Schema{Type: huma.TypeString, MinLength: &nonempty, Description: "Five fields in order: minute, hour, day of month, calendar month, and weekday (0 is Sunday)."})
		add("timezone", &huma.Schema{Type: huma.TypeString, MinLength: &nonempty, Description: "IANA time zone, e.g. Asia/Shanghai."})
	default:
		return nil
	}
	return &huma.Schema{Type: huma.TypeObject, Properties: properties, Required: required, AdditionalProperties: false}
}

// next returns the absolute once time or the first recurring instant strictly
// after both the activation anchor and the evaluation time.
func (s *ScheduledTaskSchedule) next(anchor, after time.Time) (time.Time, error) {
	if s.Type == "once" {
		due, err := time.Parse(time.RFC3339Nano, s.RunAt)
		if err != nil || !scheduledRunAtPattern.MatchString(s.RunAt) || due.UTC().Year() < 0 || due.UTC().Year() > maxScheduleYear {
			return time.Time{}, problem("invalid_argument", "schedule.runAt must be RFC3339 with Z or a numeric offset")
		}
		return due.UTC(), nil
	}
	if after.Before(anchor) {
		after = anchor
	}
	var candidate time.Time
	var err error
	switch s.Type {
	case "interval":
		candidate, err = s.nextInterval(anchor, after)
	case "daily", "weekly", "monthly", "cron":
		var location *time.Location
		location, err = time.LoadLocation(s.Timezone)
		if err != nil || s.Timezone == "" || s.Timezone == "Local" {
			return time.Time{}, problem("invalid_argument", "schedule.timezone must be an explicit IANA time zone")
		}
		if s.Type == "cron" {
			candidate, err = s.nextCron(after.In(location))
		} else {
			candidate, err = s.nextCalendar(anchor.In(location), after.In(location))
		}
	default:
		return time.Time{}, problem("invalid_argument", "unsupported schedule.type")
	}
	if err != nil {
		return time.Time{}, err
	}
	if !candidate.After(after) || candidate.UTC().Year() > maxScheduleYear {
		return time.Time{}, errScheduleExhausted
	}
	return candidate.UTC(), nil
}

func (s *ScheduledTaskSchedule) nextInterval(anchor, after time.Time) (time.Time, error) {
	var every string
	if err := json.Unmarshal(s.Every, &every); err != nil {
		return time.Time{}, problem("invalid_argument", "schedule.every must be a positive duration string")
	}
	duration, err := time.ParseDuration(every)
	if err != nil || duration <= 0 {
		return time.Time{}, problem("invalid_argument", "schedule.every must be a positive duration string")
	}
	elapsed := after.Sub(anchor)
	if elapsed < time.Duration(1<<63-1) {
		return after.Add(duration - elapsed%duration), nil
	}
	// time.Sub saturates at roughly 292 years. Preserve the interval phase
	// throughout the timestamp range without narrowing Unix seconds to nanoseconds.
	nanos := big.NewInt(after.Unix() - anchor.Unix())
	nanos.Mul(nanos, big.NewInt(int64(time.Second)))
	nanos.Add(nanos, big.NewInt(int64(after.Nanosecond()-anchor.Nanosecond())))
	remainder := nanos.Mod(nanos, big.NewInt(int64(duration))).Int64()
	return after.Add(duration - time.Duration(remainder)), nil
}

// Calendar dates are represented at midnight in UTC for arithmetic. Conversion
// to an actual instant happens only after choosing a local date and time.
func scheduleDate(at time.Time) time.Time {
	year, month, day := at.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func calendarPeriod(kind string, date time.Time) int64 {
	switch kind {
	case "daily":
		return date.Unix() / 86400
	case "weekly":
		// Start each week on Monday. 1970-01-05 is the zero week.
		monday := date.AddDate(0, 0, -(int(date.Weekday())+6)%7)
		return (monday.Unix()/86400 - 4) / 7
	default:
		return int64(date.Year()*12 + int(date.Month()) - 1)
	}
}

func (s *ScheduledTaskSchedule) nextCalendar(anchor, after time.Time) (time.Time, error) {
	every := int64(1)
	if len(s.Every) != 0 && (json.Unmarshal(s.Every, &every) != nil || every < 1) {
		return time.Time{}, problem("invalid_argument", "schedule.every must be a positive integer")
	}
	minutes := make([]int, 0, len(s.Times))
	for _, value := range s.Times {
		parsed, err := time.Parse("15:04", value)
		if err != nil || !scheduledTimePattern.MatchString(value) {
			return time.Time{}, problem("invalid_argument", "schedule.times must contain HH:mm values")
		}
		minutes = append(minutes, parsed.Hour()*60+parsed.Minute())
	}
	if len(minutes) == 0 {
		return time.Time{}, problem("invalid_argument", "schedule.times is required")
	}
	slices.Sort(minutes)
	minutes = slices.Compact(minutes)
	var days []int
	switch s.Type {
	case "weekly":
		for _, value := range s.Weekdays {
			day := slices.Index(scheduledWeekdays, value)
			if day == -1 {
				return time.Time{}, problem("invalid_argument", "schedule.weekdays must contain weekdays from mon through sun")
			}
			days = append(days, (day+6)%7+1)
		}
	case "monthly":
		for _, day := range s.Days {
			if day == 0 || day < -31 || day > 31 {
				return time.Time{}, problem("invalid_argument", "schedule.days must contain numbers from 1 to 31 or -31 to -1")
			}
		}
		days = s.Days
	default:
		days = []int{1}
	}
	if len(days) == 0 {
		return time.Time{}, problem("invalid_argument", "schedule must select at least one day")
	}
	start := calendarPeriod(s.Type, scheduleDate(anchor))
	period := calendarPeriod(s.Type, scheduleDate(after))
	last := calendarPeriod(s.Type, time.Date(maxScheduleYear, 12, 31, 0, 0, 0, 0, time.UTC))
	if remainder := (period - start) % every; remainder != 0 {
		if skip := every - remainder; skip <= last-period {
			period += skip
		} else {
			return time.Time{}, nil
		}
	}
	for period <= last {
		first, length := periodDays(s.Type, period)
		var dayMask uint32
		for _, day := range days {
			if day < 0 {
				day = length + day + 1
			}
			if day > 0 && day <= length {
				dayMask |= 1 << (day - 1)
			}
		}
		for dayMask != 0 {
			index := bits.TrailingZeros32(dayMask)
			date := first.AddDate(0, 0, index)
			if candidate := nextWallTime(date, minutes, after); !candidate.IsZero() {
				return candidate, nil
			}
			dayMask &^= 1 << index
		}
		if every > last-period {
			break
		}
		period += every
	}
	return time.Time{}, nil
}

func periodDays(kind string, period int64) (time.Time, int) {
	switch kind {
	case "daily":
		return time.Unix(period*86400, 0).UTC(), 1
	case "weekly":
		return time.Unix((period*7+4)*86400, 0).UTC(), 7
	default:
		first := time.Date(int(period/12), time.Month(period%12+1), 1, 0, 0, 0, 0, time.UTC)
		return first, first.AddDate(0, 1, -1).Day()
	}
}

func (s *ScheduledTaskSchedule) nextCron(after time.Time) (time.Time, error) {
	fields := strings.Fields(s.Expression)
	if len(fields) != 5 {
		return time.Time{}, problem("invalid_argument", "schedule.expression must contain five cron fields")
	}
	for _, field := range fields {
		if !scheduledCronField.MatchString(field) {
			return time.Time{}, problem("invalid_argument", "schedule.expression contains an unsupported cron field")
		}
	}
	parsed, err := scheduledCronParser.Parse(s.Expression)
	if err != nil {
		return time.Time{}, problem("invalid_argument", "invalid schedule.expression: "+err.Error())
	}
	spec := parsed.(*cron.SpecSchedule)
	minutes := make([]int, 0, minutesPerDay)
	for hour := range 24 {
		if spec.Hour&(1<<hour) == 0 {
			continue
		}
		for minute := range 60 {
			if spec.Minute&(1<<minute) != 0 {
				minutes = append(minutes, hour*60+minute)
			}
		}
	}
	const wildcard = uint64(1 << 63) // robfig keeps the wildcard flag in the high bit.
	// Gregorian dates and weekdays repeat every 400 years. IANA future rules
	// also repeat on this cycle, which includes skipped century leap years.
	lastYear := min(after.Year()+400, maxScheduleYear)
	for date := scheduleDate(after); date.Year() <= lastYear; date = date.AddDate(0, 0, 1) {
		if spec.Month&(1<<date.Month()) == 0 {
			continue
		}
		dayOfMonth := spec.Dom&(1<<date.Day()) != 0
		dayOfWeek := spec.Dow&(1<<date.Weekday()) != 0
		matches := dayOfMonth || dayOfWeek
		if spec.Dom&wildcard != 0 || spec.Dow&wildcard != 0 {
			matches = dayOfMonth && dayOfWeek
		}
		if matches {
			if candidate := nextWallTime(date, minutes, after); !candidate.IsZero() {
				return candidate, nil
			}
		}
	}
	return time.Time{}, nil
}

func nextWallTime(date time.Time, minutes []int, after time.Time) time.Time {
	afterDate := scheduleDate(after)
	if date.Before(afterDate) {
		return time.Time{}
	}
	if date.Equal(afterDate) {
		index, _ := slices.BinarySearch(minutes, after.Hour()*60+after.Minute()+1)
		minutes = minutes[index:]
	}
	for _, minute := range minutes {
		wall := date.Add(time.Duration(minute) * time.Minute)
		if candidate := firstWallInstant(wall, after.Location()); candidate.After(after) {
			return candidate
		}
	}
	return time.Time{}
}

func firstWallInstant(wall time.Time, location *time.Location) time.Time {
	// UTC offsets in IANA zones have magnitude less than 24 hours. Walk the
	// actual zone periods near the wall value to handle gaps and overlaps,
	// including changes by amounts other than an hour.
	const offsetWindow = 24 * time.Hour
	last := wall.Add(offsetWindow)
	for period := wall.Add(-offsetWindow).In(location); !period.After(last); {
		_, offset := period.Zone()
		_, end := period.ZoneBounds()
		if !end.IsZero() && !end.After(period) {
			// Go's extrapolated zone bounds can end after 365 days even in a
			// leap year. Continue through the last UTC day of that year.
			end = time.Date(period.UTC().Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC).In(location)
		}
		instant := wall.Add(-time.Duration(offset) * time.Second)
		if !instant.Before(period) && (end.IsZero() || instant.Before(end)) {
			return instant
		}
		if end.IsZero() {
			break
		}
		period = end
	}
	return time.Time{}
}

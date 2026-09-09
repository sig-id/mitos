package service

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

var zoneName = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`)
var cronCache = struct {
	sync.Mutex
	entries map[string]*Cron
}{entries: map[string]*Cron{}}

// Cron retains the five-field service matcher: every field must match. Weekdays
// are 0 (Sunday) through 6. Wildcard steps use value modulo step, including months.
type Cron struct {
	fields   [5]uint64
	location *time.Location
}

func CompileCron(expression, zone string) (*Cron, error) {
	if zone == "" {
		zone = "UTC"
	}
	if len(expression) > 256 || len(zone) > 128 || zone == "Local" || !zoneName.MatchString(zone) {
		return nil, errors.New("cron requires a bounded expression and an explicit IANA timezone (default UTC)")
	}
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return nil, errors.New("cron requires minute hour day month weekday")
	}
	key := strings.Join(parts, " ") + "\x00" + zone
	cronCache.Lock()
	cached := cronCache.entries[key]
	cronCache.Unlock()
	if cached != nil {
		return cached, nil
	}
	ranges := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	c := &Cron{}
	for i, part := range parts {
		mask, err := cronField(part, ranges[i][0], ranges[i][1])
		if err != nil {
			return nil, fmt.Errorf("cron field %d: %w", i+1, err)
		}
		c.fields[i] = mask
	}
	// Gregorian month lengths repeat, so a leap year proves date feasibility.
	possible := false
	for month := 1; month <= 12; month++ {
		if !c.matches(3, month) {
			continue
		}
		days := time.Date(2000, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
		for day := 1; day <= days; day++ {
			if c.matches(2, day) {
				possible = true
				break
			}
		}
	}
	if !possible {
		return nil, errors.New("cron day/month fields select no calendar date")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("unknown cron timezone %q", zone)
	}
	c.location = loc
	cronCache.Lock()
	if len(cronCache.entries) >= 256 {
		cronCache.entries = map[string]*Cron{}
	}
	cronCache.entries[key] = c
	cronCache.Unlock()
	return c, nil
}
func cronNumber(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty number")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("cron fields use numeric values")
		}
	}
	n, err := strconv.ParseUint(s, 10, 31)
	return int(n), err
}
func cronField(field string, minimum, maximum int) (uint64, error) {
	mask := uint64(0)
	for _, item := range strings.Split(field, ",") {
		parts := strings.Split(item, "/")
		if len(parts) > 2 {
			return 0, errors.New("invalid step")
		}
		step := 1
		if len(parts) == 2 {
			n, err := cronNumber(parts[1])
			if err != nil || n == 0 {
				return 0, errors.New("step must be a positive integer")
			}
			step = n
		}
		base := parts[0]
		start, end, origin := minimum, maximum, 0
		if base != "*" {
			values := strings.Split(base, "-")
			if len(values) > 2 {
				return 0, errors.New("invalid range")
			}
			n, err := cronNumber(values[0])
			if err != nil {
				return 0, errors.New("invalid numeric field")
			}
			start = n
			origin = n
			end = n
			if len(values) == 2 {
				end, err = cronNumber(values[1])
				if err != nil {
					return 0, errors.New("invalid range end")
				}
			} else if len(parts) == 2 {
				end = maximum
			}
		}
		if start < minimum || end > maximum || end < start {
			return 0, fmt.Errorf("values must form an ascending range within %d–%d", minimum, maximum)
		}
		for n := start; n <= end; n++ {
			if (n-origin)%step == 0 {
				mask |= uint64(1) << n
			}
		}
	}
	if mask == 0 {
		return 0, errors.New("field selects no values")
	}
	return mask, nil
}
func (c *Cron) matches(field, value int) bool { return c.fields[field]&(uint64(1)<<value) != 0 }

// Offsets around a local date allow exact inversion of wall time. Checking each
// candidate against the location excludes gaps and includes both sides of folds,
// including transitions that are not a whole hour.
func (c *Cron) offsets(date time.Time) ([]int, error) {
	cursor, end := date.Add(-26*time.Hour), date.Add(50*time.Hour)
	out := []int{}
	seen := map[int]bool{}
	for cursor.Before(end) {
		zoned := cursor.In(c.location)
		_, offset := zoned.Zone()
		if offset < -24*3600 || offset > 24*3600 {
			return nil, errors.New("unsupported timezone offset")
		}
		if !seen[offset] {
			seen[offset] = true
			out = append(out, offset)
		}
		_, boundary := zoned.ZoneBounds()
		if boundary.IsZero() || !boundary.Before(end) {
			break
		}
		if !boundary.After(cursor) {
			return nil, errors.New("timezone transition did not advance")
		}
		cursor = boundary
	}
	return out, nil
}

// Next returns the first matching instant strictly after now. A full Gregorian
// cycle covers rare day/weekday conjunctions without minute-by-minute scanning.
func (c *Cron) Next(now time.Time) (time.Time, error) {
	local := now.In(c.location)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	limit := date.AddDate(400, 0, 0)
	for date.Before(limit) {
		if c.matches(3, int(date.Month())) && c.matches(2, date.Day()) && c.matches(4, int(date.Weekday())) {
			offsets, err := c.offsets(date)
			if err != nil {
				return time.Time{}, err
			}
			best := time.Time{}
			for hour := 0; hour < 24; hour++ {
				if !c.matches(1, hour) {
					continue
				}
				for minute := 0; minute < 60; minute++ {
					if !c.matches(0, minute) {
						continue
					}
					wall := date.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
					for _, offset := range offsets {
						instant := wall.Add(-time.Duration(offset) * time.Second)
						if !instant.After(now) || !best.IsZero() && !instant.Before(best) {
							continue
						}
						check := instant.In(c.location)
						if check.Year() == date.Year() && check.Month() == date.Month() && check.Day() == date.Day() && check.Hour() == hour && check.Minute() == minute && check.Second() == 0 {
							best = instant
						}
					}
				}
			}
			if !best.IsZero() {
				return best.UTC(), nil
			}
		}
		date = date.AddDate(0, 0, 1)
	}
	return time.Time{}, errors.New("cron has no matching instant in a Gregorian calendar cycle")
}

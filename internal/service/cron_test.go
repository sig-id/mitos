package service

import (
	"sync"
	"testing"
	"time"
)

func cronInstant(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestCronFieldsCalendarAndRetainedConjunction(t *testing.T) {
	cases := []struct{ expr, zone, after, want string }{
		{"*/15 9-17 * * 1-5", "UTC", "2026-09-09T09:00:00Z", "2026-09-09T09:15:00Z"},
		{"5,35 9 * * *", "UTC", "2026-09-09T09:35:00Z", "2026-09-10T09:05:00Z"},
		{"0 9 13 * 5", "UTC", "2026-01-01T00:00:00Z", "2026-02-13T09:00:00Z"},
		{"0 0 */2 */2 *", "UTC", "2026-01-01T00:00:00Z", "2026-02-02T00:00:00Z"},
		{"0 0 29 2 1", "UTC", "2026-01-01T00:00:00Z", "2044-02-29T00:00:00Z"},
		{"0 0 29 2 *", "UTC", "2099-01-01T00:00:00Z", "2104-02-29T00:00:00Z"},
		{"0 9 * * *", "Asia/Kathmandu", "2026-09-09T02:59:00Z", "2026-09-09T03:15:00Z"},
		{"1-20/5,45 9 * * *", "", "2026-09-09T09:06:00Z", "2026-09-09T09:11:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.expr+tc.zone, func(t *testing.T) {
			c, err := CompileCron(tc.expr, tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Next(cronInstant(t, tc.after))
			if err != nil || !got.Equal(cronInstant(t, tc.want)) {
				t.Fatalf("got %s, want %s: %v", got, tc.want, err)
			}
		})
	}
}
func TestCronDSTGapsFoldsAndHalfHourTransitions(t *testing.T) {
	cases := []struct{ expr, zone, after, want string }{
		{"30 2 * * *", "Europe/Zurich", "2026-03-28T01:30:00Z", "2026-03-30T00:30:00Z"},
		{"30 2 * * *", "Europe/Zurich", "2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z"},
		{"15,45 2 * * *", "Europe/Zurich", "2026-10-25T00:20:00Z", "2026-10-25T00:45:00Z"},
		{"15,45 2 * * *", "Europe/Zurich", "2026-10-25T00:45:00Z", "2026-10-25T01:15:00Z"},
		{"15 2 * * *", "Australia/Lord_Howe", "2026-10-02T15:45:00Z", "2026-10-04T15:15:00Z"},
		{"45 1 * * *", "Australia/Lord_Howe", "2026-04-04T14:45:00Z", "2026-04-04T15:15:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.zone+tc.after, func(t *testing.T) {
			c, err := CompileCron(tc.expr, tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Next(cronInstant(t, tc.after))
			if err != nil || !got.Equal(cronInstant(t, tc.want)) {
				t.Fatalf("got %s, want %s: %v", got, tc.want, err)
			}
		})
	}
}
func TestCronRejectsInvalidAndUnsatisfiableDefinitions(t *testing.T) {
	for _, expr := range []string{"* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 7", "*/0 * * * *", "-1 * * * *", "5-2 * * * *", "1,,2 * * * *", "* * * JAN *", "* * 30 2 *", "* * 31 4,6 *", "*/999999999999999999999999 * * * *"} {
		if _, err := CompileCron(expr, "UTC"); err == nil {
			t.Fatal("invalid cron accepted", expr)
		}
	}
	for _, zone := range []string{"Local", "../UTC", "/etc/localtime", "Missing/Zone"} {
		if _, err := CompileCron("* * * * *", zone); err == nil {
			t.Fatal("invalid timezone accepted", zone)
		}
	}
	// March 31 on Sunday is always the Zurich spring transition; 02:30 is absent.
	c, err := CompileCron("30 2 31 3 0", "Europe/Zurich")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Next(cronInstant(t, "2026-01-01T00:00:00Z")); err == nil {
		t.Fatal("nonexistent wall-time calendar accepted")
	}
}
func TestCronConcurrentCompilationAndScheduleValidation(t *testing.T) {
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := CompileCron("*/5 * * * *", "UTC")
			if err == nil {
				_, err = c.Next(time.Now())
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []Unit{
		{Trigger: "cron", Schedule: &Schedule{Type: "cron", Cron: "* * * * *", Interval: 1}},
		{Trigger: "cron", Schedule: &Schedule{Type: "cron", Cron: "* * * * *", Jitter: 1}},
		{Trigger: "interval", Schedule: &Schedule{Type: "interval", Interval: 1, Timezone: "UTC"}},
	} {
		if u.ValidateRuntime() == nil {
			t.Fatal("mixed schedule fields accepted")
		}
	}
}

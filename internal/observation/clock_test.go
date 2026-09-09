package observation

import (
	"reflect"
	"testing"
	"time"
)

func TestClockEdgesAndPollingLoop(t *testing.T) {
	start := time.Unix(0, 0)
	thresholds := Thresholds{10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond}
	c, err := NewClock(start, thresholds)
	if err != nil {
		t.Fatal(err)
	}
	sample := Sample{Execs: []string{ArgvDigest([]byte("sleep\x001\x00"))}, TreeSize: 2}
	var actual []Edge
	for ms := 0; ms <= 100; ms += 5 {
		edges, err := c.Observe(start.Add(time.Duration(ms)*time.Millisecond), sample)
		if err != nil {
			t.Fatal(err)
		}
		actual = append(actual, edges...)
	}
	if len(actual) != 3 || actual[2].Threshold != "block" || actual[2].ForMS != 50 {
		t.Fatalf("polling reset clock or duplicate edges: %+v", actual)
	}
	// Replay must reproduce the same evidence for identical time/sample pairs.
	replay, _ := NewClock(start, thresholds)
	var reproduced []Edge
	for ms := 0; ms <= 100; ms += 5 {
		edges, _ := replay.Observe(start.Add(time.Duration(ms)*time.Millisecond), sample)
		reproduced = append(reproduced, edges...)
	}
	if !reflect.DeepEqual(actual, reproduced) {
		t.Fatal("clock is not replayable")
	}
	sample.WorkspaceWrites++
	edges, err := c.Observe(start.Add(101*time.Millisecond), sample)
	if err != nil || len(edges) != 0 {
		t.Fatalf("write did not reset quiet period: %v %v", edges, err)
	}
	edges, err = c.Observe(start.Add(151*time.Millisecond), sample)
	if err != nil || len(edges) != 3 || edges[0].WorkspaceWritesSince != 1 {
		t.Fatalf("new quiet period missing: %v %v", edges, err)
	}
}

func TestClockTracksActivityAndRejectsInvalidInputs(t *testing.T) {
	start := time.Unix(0, 0)
	if _, err := NewClock(start, Thresholds{1, 1, 2}); err == nil {
		t.Fatal("unordered thresholds accepted")
	}
	c, _ := NewClock(start, Thresholds{10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond})
	sample := Sample{PTYOffset: 1, OutputOffset: 1, WorkspaceWrites: 1}
	c.Observe(start, sample)
	sample.Execs = []string{"new-command"}
	if edges, err := c.Observe(start.Add(40*time.Millisecond), sample); err != nil || len(edges) != 0 {
		t.Fatal("new exec did not reset clock")
	}
	if _, err := c.Observe(start, sample); err == nil {
		t.Fatal("time reversal accepted")
	}
	sample.PTYOffset = 0
	if _, err := c.Observe(start.Add(time.Second), sample); err == nil {
		t.Fatal("counter reversal accepted")
	}
	thresholds, err := FromThinkTime(time.Minute)
	if err != nil || thresholds.Block != 2*time.Minute {
		t.Fatalf("think time not respected: %v %v", thresholds, err)
	}
}

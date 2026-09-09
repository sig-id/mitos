package decision

import (
	"encoding/json"
	"testing"
	"time"
)

func card() Card {
	return Card{ID: "d", RunID: "r", CausedBy: 1, Tenant: LocalTenant, Kind: "choose", Reversibility: "external", Question: "Choose the next action.", Context: Context{Attempted: "Run the brief", BlockedBy: "A fact is missing", EvidenceRefs: []string{"output.txt"}}, Options: []Option{{ID: "retry", Label: "Retry", Consequence: "Run again"}, {ID: "cancel", Label: "Close", Consequence: "Keep stopped"}}, RequestedAt: time.Unix(100, 0), DedupeKey: "key"}
}
func TestSilenceRulesAndCanonicalShape(t *testing.T) {
	c := card()
	if err := c.Validate(false); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(c)
	var raw map[string]json.RawMessage
	json.Unmarshal(encoded, &raw)
	for _, key := range []string{"default", "ttl_ms", "value_schema"} {
		if string(raw[key]) != "null" {
			t.Fatalf("%s is not explicitly null", key)
		}
	}
	choice := "cancel"
	ttl := int64(1000)
	c.Default = &choice
	c.TTLMS = &ttl
	if c.Validate(false) == nil {
		t.Fatal("silence authorized an external act")
	}
	c.Reversibility = "recoverable"
	if err := c.Validate(false); err != nil {
		t.Fatal(err)
	}
	if c.Validate(true) == nil {
		t.Fatal("Market accepted a default")
	}
	if c.Due(c.RequestedAt.Add(999*time.Millisecond)) || !c.Due(c.RequestedAt.Add(time.Second)) {
		t.Fatal("TTL boundary is wrong")
	}
	c.Choice = "cancel"
	if c.Due(c.RequestedAt.Add(time.Hour)) {
		t.Fatal("resolved default fired again")
	}
	c.Choice = ""
	c.Default = nil
	if c.Validate(false) == nil {
		t.Fatal("TTL without default accepted")
	}
}
func TestCardRefusesUnexecutableOrAmbiguousShape(t *testing.T) {
	for _, mutate := range []func(*Card){
		func(c *Card) { c.Reversibility = "internal" },
		func(c *Card) { c.Tenant = Tenant{} },
		func(c *Card) { c.Options[1].ID = c.Options[0].ID },
		func(c *Card) { c.Options[0].Consequence = "" },
		func(c *Card) { c.Options[0].Recommended = true; c.Options[1].Recommended = true },
		func(c *Card) { c.Context.Attempted = "" },
		func(c *Card) { c.CausedBy = 0 },
		func(c *Card) { c.Kind = "supply_value" },
	} {
		c := card()
		mutate(&c)
		if c.Validate(false) == nil {
			t.Fatalf("invalid card accepted: %+v", c)
		}
	}
}
func TestLegacyStringContextRemainsReadable(t *testing.T) {
	var c Card
	if err := json.Unmarshal([]byte(`{"context":"old context","cost_of_wrong":"old consequence","reversibility":"external"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Context.BlockedBy != "old context" || c.CostOfWrong.Detail != "old consequence" || c.Default != nil {
		t.Fatal("legacy card changed meaning")
	}
}
func TestClassificationUsesConfinementAndExactCompensationGrants(t *testing.T) {
	grants := Grants{WorkspaceConfined: true, CheckpointAvailable: true, Destinations: map[string]DestinationGrant{"owned-mirror": {Compensatable: true}, "upstream": {Compensatable: true, RequiresApproval: true}}, SpendLimit: 1}
	cases := []struct {
		act   Act
		class string
	}{
		{Act{Paths: []string{"src/main.go"}}, "internal"},
		{Act{Paths: []string{"../outside"}}, "external"},
		{Act{Destinations: []string{"owned-mirror"}}, "recoverable"},
		{Act{Destinations: []string{"owned-mirror/other"}}, "external"},
		{Act{Destinations: []string{"upstream"}}, "external"},
		{Act{Spend: 1}, "external"},
		{Act{Paths: []string{"src"}, Judgment: true}, "external"},
		{Act{Paths: []string{"src"}, MissingInput: true}, "external"},
		{Act{Unknown: true}, "external"},
	}
	for _, c := range cases {
		if got := Classify(c.act, grants); got.Reversibility != c.class {
			t.Fatalf("%+v: %+v", c.act, got)
		}
	}
	grants.WorkspaceConfined = false
	if Classify(Act{Paths: []string{"src"}}, grants).Reversibility != "external" {
		t.Fatal("same-user process earned confinement")
	}
	grants.WorkspaceConfined = true
	grants.CheckpointAvailable = false
	if Classify(Act{Paths: []string{"src"}}, grants).Reversibility != "external" {
		t.Fatal("unrecoverable change classified internal")
	}
}

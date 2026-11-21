package watch

import (
	"context"
	"errors"
	"testing"

	"mitos/internal/decisionmodel"
)

func triageRequest(offered []Level) decisionmodel.Request {
	r := decisionmodel.Request{ID: "triage_1", State: []byte(`{}`)}
	for _, l := range offered {
		r.Choices = append(r.Choices, decisionmodel.Choice{Value: string(l), Description: string(l)})
	}
	return r
}

func answer(choice Level, confidence, p float64, offered []Level) *decisionmodel.Answer {
	a := &decisionmodel.Answer{Model: "m", Choice: string(choice), Confidence: confidence, Probabilities: map[string]float64{}}
	rest := (1 - p) / float64(len(offered)-1)
	for _, l := range offered {
		a.Probabilities[string(l)] = rest
	}
	a.Probabilities[string(choice)] = p
	return a
}

func TestAcceptGradesEachOutcome(t *testing.T) {
	offered := Triage{}.Offered(Notify)
	req := triageRequest(offered)
	th := Thresholds{MinConfidence: .8, MinProbability: .7}
	wrongSum := answer(Digest, .9, .9, offered)
	wrongSum.Probabilities[string(Wake)] = .5
	cases := []struct {
		name   string
		answer *decisionmodel.Answer
		err    error
		want   Grade
	}{
		{"raise", answer(Wake, .1, .4, offered), nil, Grade{Wake, SourceTriage, ""}},
		{"same", answer(Notify, .1, .4, offered), nil, Grade{Notify, SourceTriage, ""}},
		{"lower confident", answer(Digest, .9, .8, offered), nil, Grade{Digest, SourceTriage, ""}},
		{"lower unsure", answer(Digest, .5, .8, offered), nil, Grade{Notify, SourceFallback, FallbackBelowThreshold}},
		{"lower improbable", answer(Digest, .9, .5, offered), nil, Grade{Notify, SourceFallback, FallbackBelowThreshold}},
		{"not offered", answer(Escalate, .9, .9, append(offered, Escalate)), nil, Grade{Notify, SourceFallback, FallbackInvalidAnswer}},
		{"invalid probabilities", wrongSum, nil, Grade{Notify, SourceFallback, FallbackInvalidAnswer}},
		{"missing answer", nil, nil, Grade{Notify, SourceFallback, FallbackInvalidAnswer}},
		{"refused", nil, errors.New("model_refused"), Grade{Notify, SourceFallback, FallbackRefused}},
		{"interrupted", nil, errors.New("request_interrupted"), Grade{Notify, SourceFallback, FallbackTimeout}},
		{"deadline", nil, context.DeadlineExceeded, Grade{Notify, SourceFallback, FallbackTimeout}},
		{"credential", nil, errors.New("credential_unavailable"), Grade{Notify, SourceFallback, FallbackCredential}},
		{"bad response", nil, errors.New("invalid_answers"), Grade{Notify, SourceFallback, FallbackInvalidAnswer}},
		{"http", nil, errors.New("http_503"), Grade{Notify, SourceFallback, FallbackProviderError}},
	}
	for _, c := range cases {
		if got := Accept(c.answer, c.err, req, Notify, offered, th); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestOfferedLevels(t *testing.T) {
	if got := (Triage{}).Offered(Notify); len(got) != 3 {
		t.Fatalf("escalate offered without permission: %v", got)
	}
	if got := (Triage{AllowEscalate: true}).Offered(Notify); len(got) != 4 {
		t.Fatalf("escalate not offered: %v", got)
	}
	if got := (Triage{}).Offered(Escalate); len(got) != 4 {
		t.Fatalf("the default must always be offered: %v", got)
	}
}

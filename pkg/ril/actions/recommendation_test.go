package actions

import (
	"testing"
	"time"
)

func TestDecideAcceptance(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	open := Recommendation{State: RecommendationOpen, ProposalDigest: "sha256:p", MaterialDigest: "sha256:m", ExpiresAt: now.Add(time.Hour)}
	request := AcceptRecommendation{ProposalDigest: "sha256:p", Fresh: ProposeRecommendation{MaterialDigest: "sha256:m"}, CorrelationID: "run-1"}
	acceptedBy := func(run string) Recommendation {
		r := open
		r.State, r.Acceptance = RecommendationAccepted, &Acceptance{AuditCorrelation: run}
		return r
	}
	cases := []struct {
		name    string
		current Recommendation
		request AcceptRecommendation
		want    acceptDecision
	}{
		{"equal decision on the fresh snapshot accepts", open, request, decideAccept},
		{"material change supersedes", open, AcceptRecommendation{ProposalDigest: "sha256:p", Fresh: ProposeRecommendation{MaterialDigest: "sha256:other"}}, decideSupersede},
		{"advice not shown is refused", open, AcceptRecommendation{ProposalDigest: "sha256:else", Fresh: request.Fresh}, decideRefuseDigest},
		{"expired advice is never accepted as shown", func() Recommendation { r := open; r.ExpiresAt = now; return r }(), request, decideRefuseState},
		{"the accepting run replays", acceptedBy("run-1"), request, decideReplay},
		{"another run cannot reuse an acceptance", acceptedBy("run-0"), request, decideRefuseState},
		{"rejected advice cannot be accepted", func() Recommendation { r := open; r.State = RecommendationRejected; return r }(), request, decideRefuseState},
		{"superseded advice cannot be accepted", func() Recommendation { r := open; r.State = RecommendationSuperseded; return r }(), request, decideRefuseState},
	}
	for _, tc := range cases {
		if got := decideAcceptance(tc.current, tc.request, now); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestLazyState(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	if got := lazyState(Recommendation{State: RecommendationOpen, ExpiresAt: past}, now); got != RecommendationExpired {
		t.Errorf("open past expiry: %s", got)
	}
	if got := lazyState(Recommendation{State: RecommendationSnoozed, SnoozedUntil: &past, ExpiresAt: future}, now); got != RecommendationOpen {
		t.Errorf("snooze ended: %s", got)
	}
	if got := lazyState(Recommendation{State: RecommendationSnoozed, SnoozedUntil: &future, ExpiresAt: future}, now); got != RecommendationSnoozed {
		t.Errorf("snooze running: %s", got)
	}
	if got := lazyState(Recommendation{State: RecommendationAccepted, ExpiresAt: past}, now); got != RecommendationAccepted {
		t.Errorf("accepted advice never expires: %s", got)
	}
}

package subtitles

import (
	"context"
	"strings"
	"testing"

	"cineroute/internal/subtitles/opensubtitles"
)

// TestYearToleranceAcceptsOneYearDifference pins the release-year rule: a
// candidate may be one year away from the movie and still be the same film,
// because films open in different countries in different years.
func TestYearToleranceAcceptsOneYearDifference(t *testing.T) {
	reference := Reference{Basename: "Revenge.2018.1080p.AMZN.WEB-DL", Title: "Revenge", Year: 2018}

	exact := languageCandidate("Revenge", 2018, "sv", "Revenge.2018.1080p.WEB-DL", 900)
	oneOff := languageCandidate("Revenge", 2017, "sv", "Revenge.2017.1080p.WEB-DL", 901)
	twoOff := languageCandidate("Revenge", 2016, "sv", "Revenge.2016.1080p.WEB-DL", 902)

	exactScore, exactReasons, _, ok := ScoreCandidate(reference, exact)
	if !ok {
		t.Fatal("the exact-year candidate must score")
	}
	oneOffScore, oneOffReasons, _, ok := ScoreCandidate(reference, oneOff)
	if !ok {
		t.Fatal("the one-year-off candidate must score")
	}
	twoOffScore, twoOffReasons, _, _ := ScoreCandidate(reference, twoOff)

	if containsReason(twoOffReasons, "YEAR MISMATCH") != true {
		t.Fatalf("two years off reasons = %v, want a year mismatch", twoOffReasons)
	}
	if containsReason(oneOffReasons, "YEAR MISMATCH") {
		t.Errorf("one year off reasons = %v, want no year mismatch", oneOffReasons)
	}
	if containsReason(exactReasons, "YEAR MISMATCH") {
		t.Errorf("exact year reasons = %v, want no year mismatch", exactReasons)
	}
	if oneOffScore <= twoOffScore {
		t.Errorf("score one year off = %.0f, two years off = %.0f; the closer year must win", oneOffScore, twoOffScore)
	}
	// An exact match still outranks a one-year difference, but only slightly.
	if exactScore <= oneOffScore {
		t.Errorf("exact year score = %.0f, one year off = %.0f; the exact match must stay ahead", exactScore, oneOffScore)
	}
	if exactScore-oneOffScore > 50 {
		t.Errorf("gap between exact and one-year-off = %.0f, want a small preference", exactScore-oneOffScore)
	}
}

// TestYearToleranceKeepsTheAuditStrong checks the same rule in the audit, which
// is what decides whether the page calls a candidate strong or "review": a
// one-year difference must not push a matching candidate into review, while a
// two-year difference still does.
func TestYearToleranceKeepsTheAuditStrong(t *testing.T) {
	reference := Reference{Basename: "Revenge.2018.1080p.AMZN.WEB-DL", Title: "Revenge", Year: 2018}

	oneOff := languageCandidate("Revenge", 2017, "sv", "Revenge.2017.1080p.WEB-DL", 910)
	category, reasons := AuditCandidate(reference, oneOff, releaseName(oneOff))
	if category == CategoryReview {
		t.Fatalf("one year off was audited as review: %v", reasons)
	}
	for _, reason := range reasons {
		if strings.Contains(reason, "year") {
			t.Errorf("one year off reasons = %v, want no year complaint", reasons)
		}
	}

	twoOff := languageCandidate("Revenge", 2016, "sv", "Revenge.2016.1080p.WEB-DL", 911)
	category, reasons = AuditCandidate(reference, twoOff, releaseName(twoOff))
	if category != CategoryReview {
		t.Errorf("two years off category = %s, want review (%v)", category, reasons)
	}
	if !containsReason(reasons, "release year conflict") {
		t.Errorf("two years off reasons = %v, want a release year conflict", reasons)
	}
}

// TestYearToleranceDoesNotRescueADifferentTitle makes sure the tolerance did not
// open the door to a different movie: within a year of the reference, the title
// is the only signal left, so a different one still has to be flagged.
func TestYearToleranceDoesNotRescueADifferentTitle(t *testing.T) {
	reference := Reference{Basename: "Revenge.2018.1080p.AMZN.WEB-DL", Title: "Revenge", Year: 2018}

	// "Revenger" (2017): one year away, so the year no longer objects.
	other := languageCandidate("Revenger", 2017, "sv", "Revenger.2017.1080p.WEB-DL", 920)
	category, reasons := AuditCandidate(reference, other, releaseName(other))
	if category != CategoryReview {
		t.Fatalf("category = %s, want review for a different title (%v)", category, reasons)
	}
	if !containsReason(reasons, "approximate title") {
		t.Errorf("reasons = %v, want the title to be the reason", reasons)
	}
}

// TestRevengeYearDifferenceStillInstalls runs the reported case end to end: the
// movie is tagged 2018 by its release name while the Swedish candidates are
// 2017. They must be audited as strong (not review) and installed.
func TestRevengeYearDifferenceStillInstalls(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Revenge (2017)", "Revenge.2018.1080p.AMZN.WEB-DL.DDP2.0.H.264-GRiMM.mkv")
	h.configureEmbeddedEnglish(video)
	h.os.items = []opensubtitles.Item{
		languageCandidate("Revenge", 2017, "sv", "Revenge.2017.1080p.WEB-DL.H.264.DD+5.1-NTG", 930),
	}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
	if item.Year != 2018 {
		t.Fatalf("item year = %d, want 2018 from the release name for this test", item.Year)
	}
	status, err := h.manager.processAndPersist(context.Background(), item, runOptions{})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if status != StatusAdded {
		t.Fatalf("status = %s, want added", status)
	}
	target := item.Target("sv")
	if target == nil || target.Status != StatusAdded {
		t.Fatalf("target = %+v, want sv added", target)
	}
	for _, candidate := range h.manager.candidatesFor(item.ID) {
		if candidate.FileID != 930 {
			continue
		}
		if candidate.Category == CategoryReview {
			t.Errorf("candidate = %+v, want it out of review despite the year difference", candidate)
		}
		for _, reason := range candidate.Reasons {
			if strings.Contains(reason, "YEAR MISMATCH") {
				t.Errorf("candidate reasons = %v, want no year mismatch", candidate.Reasons)
			}
		}
	}
}

func containsReason(reasons []string, needle string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, needle) {
			return true
		}
	}
	return false
}

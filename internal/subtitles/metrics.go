package subtitles

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// AcceptCriteria is the strict timing gate from the proven second-pass pipeline.
// A candidate that fails it is never installed as <video>.sv.srt.
type AcceptCriteria struct {
	MinWithin2        float64 `json:"min_within_2s"`
	MaxP90Seconds     float64 `json:"max_p90_nearest_seconds"`
	MaxStartGapMin    float64 `json:"max_start_gap_minutes"`
	MaxEndGapMin      float64 `json:"max_end_gap_minutes"`
	MaxZeroStartCues  int     `json:"max_zero_start_cues"`
	MaxCueMinutes     float64 `json:"max_cue_minutes"`
	MaxOverrunMinutes float64 `json:"max_overrun_minutes"`
}

// DefaultAcceptCriteria returns the exact thresholds used by
// `second-pass-swedish.py::metrics`.
func DefaultAcceptCriteria() AcceptCriteria {
	return AcceptCriteria{
		MinWithin2:        0.90,
		MaxP90Seconds:     2.5,
		MaxStartGapMin:    10,
		MaxEndGapMin:      10,
		MaxZeroStartCues:  3,
		MaxCueMinutes:     10,
		MaxOverrunMinutes: 15,
	}
}

// Metrics is the per-attempt timing report shown in the UI and used to accept a
// synchronized subtitle.
type Metrics struct {
	Cues             int      `json:"cues"`
	P50              float64  `json:"p50"`
	P90              float64  `json:"p90"`
	Within2          float64  `json:"within_2s"`
	StartGap         float64  `json:"start_gap_minutes"`
	EndGap           float64  `json:"end_gap_minutes"`
	Overrun          float64  `json:"overrun_minutes"`
	ZeroStartCues    int      `json:"zero_start_cues"`
	LongestCue       float64  `json:"longest_cue_minutes"`
	Score            float64  `json:"score"`
	Acceptable       bool     `json:"acceptable"`
	RemovedPromos    int      `json:"removed_promo_cues,omitempty"`
	RemovedMalformed int      `json:"removed_malformed_cues,omitempty"`
	ReferenceCues    int      `json:"reference_cues,omitempty"`
	AlassBlocks      int      `json:"alass_blocks,omitempty"`
	AlassCoverage    float64  `json:"alass_coverage,omitempty"`
	AlassFPS         string   `json:"alass_fps,omitempty"`
	AlassShifts      []string `json:"alass_shifts,omitempty"`
	CoarseIssues     []string `json:"coarse_issues,omitempty"`
}

// NearestMetrics ports `second-pass-swedish.py::nearest_metrics`. Distances are
// milliseconds from every cue start to the nearest reference cue start.
func NearestMetrics(subject, reference []TimeSpan) (p50, p90, within2 float64) {
	if len(subject) == 0 || len(reference) == 0 {
		return 0, 0, 0
	}
	starts := make([]int64, 0, len(reference))
	for _, span := range reference {
		starts = append(starts, span.Start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })

	distances := make([]int64, 0, len(subject))
	for _, span := range subject {
		index := sort.Search(len(starts), func(i int) bool { return starts[i] >= span.Start })
		best := int64(math.MaxInt64)
		if index < len(starts) {
			best = absInt64(starts[index] - span.Start)
		}
		if index > 0 {
			if previous := absInt64(starts[index-1] - span.Start); previous < best {
				best = previous
			}
		}
		distances = append(distances, best)
	}
	sort.Slice(distances, func(i, j int) bool { return distances[i] < distances[j] })

	count := len(distances)
	p50 = float64(distances[minInt(count-1, count/2)]) / 1000
	p90 = float64(distances[minInt(count-1, int(float64(count)*0.9))]) / 1000
	within := 0
	for _, distance := range distances {
		if distance <= 2000 {
			within++
		}
	}
	within2 = float64(within) / float64(count)
	return p50, p90, within2
}

// ComputeMetrics ports `second-pass-swedish.py::metrics`, including the score
// formula and the acceptance decision.
func ComputeMetrics(cues, reference []TimeSpan, criteria AcceptCriteria) Metrics {
	out := Metrics{Cues: len(cues), ReferenceCues: len(reference)}
	if len(cues) == 0 || len(reference) == 0 {
		return out
	}
	out.P50, out.P90, out.Within2 = NearestMetrics(cues, reference)

	refStart, refEnd := extent(reference)
	outStart, outEnd := extent(cues)
	out.StartGap = minutes(maxInt64(0, outStart-refStart))
	out.EndGap = minutes(maxInt64(0, refEnd-outEnd))
	out.Overrun = minutes(maxInt64(0, outEnd-refEnd))
	for _, span := range cues {
		if span.Start == 0 {
			out.ZeroStartCues++
		}
		if longest := minutes(span.End - span.Start); longest > out.LongestCue {
			out.LongestCue = longest
		}
	}
	out.Score = out.Within2*100 - out.P90 - 2*out.StartGap - 2*out.EndGap - 2*float64(out.ZeroStartCues) - math.Max(0, out.Overrun-2)
	out.Acceptable = out.Within2 >= criteria.MinWithin2 &&
		out.P90 <= criteria.MaxP90Seconds &&
		out.StartGap <= criteria.MaxStartGapMin &&
		out.EndGap <= criteria.MaxEndGapMin &&
		out.ZeroStartCues <= criteria.MaxZeroStartCues &&
		out.LongestCue <= criteria.MaxCueMinutes &&
		out.Overrun <= criteria.MaxOverrunMinutes
	return out
}

// CoarseIssues ports the first-pass review flags from
// `sync-swedish-subs.py::analyze_result`. They are recorded as evidence even
// when the strict metrics accept the file.
func CoarseIssues(reference, raw, output []TimeSpan, report AlassReport) []string {
	var issues []string
	if len(output) != len(raw) {
		issues = append(issues, fmt.Sprintf("cue count changed %d->%d", len(raw), len(output)))
	}
	synchronized := 0
	for _, block := range report.Blocks {
		synchronized += block.Count
	}
	switch {
	case len(report.Blocks) == 0:
		issues = append(issues, "ALASS reported no synchronized blocks")
	case len(raw) > 0 && float64(synchronized) < float64(len(raw))*0.80:
		issues = append(issues, fmt.Sprintf("low synchronized coverage %d/%d", synchronized, len(raw)))
	}
	if len(report.Blocks) > 12 {
		issues = append(issues, fmt.Sprintf("many timing blocks (%d)", len(report.Blocks)))
	}
	for _, block := range report.Blocks {
		if math.Abs(SignedTimeSeconds(block.Shift)) > 10*60 {
			issues = append(issues, "extreme timing shift ("+block.Shift+")")
			break
		}
	}
	newlyZero := countZeroStarts(output) - countZeroStarts(raw)
	if newlyZero > 0 {
		issues = append(issues, fmt.Sprintf("%d cue(s) may have been clamped to zero", newlyZero))
	}
	if len(reference) > 0 && len(output) > 0 {
		_, refEnd := extent(reference)
		_, outEnd := extent(output)
		if outEnd > refEnd+10*60*1000 {
			issues = append(issues, "output extends over 10 minutes beyond reference")
		}
	}
	return issues
}

// SignedTimeSeconds parses a signed `H:MM:SS.mmm` shift into seconds.
func SignedTimeSeconds(value string) float64 {
	if value == "" {
		return 0
	}
	sign := 1.0
	text := value
	if text[0] == '-' {
		sign = -1
		text = text[1:]
	} else if text[0] == '+' {
		text = text[1:]
	}
	parts := strings.Split(text, ":")
	if len(parts) != 3 {
		return 0
	}
	hours, _ := strconv.Atoi(parts[0])
	minutes, _ := strconv.Atoi(parts[1])
	seconds, _ := strconv.ParseFloat(parts[2], 64)
	return sign * (float64(hours)*3600 + float64(minutes)*60 + seconds)
}

func extent(spans []TimeSpan) (int64, int64) {
	start := spans[0].Start
	end := spans[0].End
	for _, span := range spans {
		if span.Start < start {
			start = span.Start
		}
		if span.End > end {
			end = span.End
		}
	}
	return start, end
}

func countZeroStarts(spans []TimeSpan) int {
	count := 0
	for _, span := range spans {
		if span.Start == 0 {
			count++
		}
	}
	return count
}

func minutes(milliseconds int64) float64 { return float64(milliseconds) / 60000 }

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

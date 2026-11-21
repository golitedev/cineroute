package requests

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"
)

const candidateAttemptLimit = 10

// SelectionReport records the ranking and outcomes of one automatic search.
type SelectionReport struct {
	At         time.Time            `json:"at"`
	Queries    []string             `json:"queries"`
	Order      []string             `json:"order"`
	Trackers   []SelectionTracker   `json:"trackers"`
	Candidates []SelectionCandidate `json:"candidates"`
}

type SelectionTracker struct {
	IndexerID int            `json:"indexer_id"`
	Name      string         `json:"name"`
	Priority  int            `json:"priority"`
	Accepted  int            `json:"accepted"`
	Rejected  map[string]int `json:"rejected,omitempty"`
}

type SelectionCandidate struct {
	Rank      int              `json:"rank"`
	IndexerID int              `json:"indexer_id"`
	Title     string           `json:"title"`
	Quality   string           `json:"quality"`
	Codec     string           `json:"codec"`
	Size      int64            `json:"size"`
	Scores    map[string]int64 `json:"scores"`
	Outcome   string           `json:"outcome"`
	Reason    string           `json:"reason,omitempty"`
}

func recordSelection(job *Job, queries []string, trackers []SelectionTracker, candidates []Candidate, settings Settings) *SelectionReport {
	const reportLimit = 3
	order := NormalizeSelectionOrder(settings.SelectionOrder)
	report := SelectionReport{At: time.Now().UTC(), Queries: queries, Order: order, Trackers: trackers,
		Candidates: make([]SelectionCandidate, 0, min(len(candidates), candidateAttemptLimit))}
	for i, candidate := range candidates[:min(len(candidates), candidateAttemptLimit)] {
		scores := make(map[string]int64, len(order))
		for _, criterion := range order {
			scores[criterion] = rankingValue(candidate, criterion, settings.PreferSeasonPacks)
		}
		report.Candidates = append(report.Candidates, SelectionCandidate{
			Rank: i + 1, IndexerID: candidate.Release.IndexerID, Title: candidate.Release.Title,
			Quality: candidate.Quality, Codec: Codec(candidate.Release.Title), Size: candidate.Release.Size, Scores: scores, Outcome: "not tried",
		})
	}
	job.SelectionReports = append(job.SelectionReports, report)
	if len(job.SelectionReports) > reportLimit {
		job.SelectionReports = job.SelectionReports[len(job.SelectionReports)-reportLimit:]
	}
	return &job.SelectionReports[len(job.SelectionReports)-1]
}

func (c *SelectionCandidate) recordFailure(requestID, outcome, reason string) {
	c.Outcome, c.Reason = outcome, reason
	message := "request candidate skipped"
	if outcome == "awaiting verification" {
		message = "request candidate awaiting verification"
	}
	slog.Info(message, "request_id", requestID, "rank", c.Rank, "indexer_id", c.IndexerID,
		"title", c.Title, "outcome", c.Outcome, "reason", c.Reason)
}

var (
	diagnosticURL   = regexp.MustCompile(`(?i)https?://[^\s]+`)
	clientErrorBody = regexp.MustCompile(`(qBittorrent (?:GET|POST) \S+ returned \d+|qBittorrent login failed \(status \d+\)): .*`)
)

func diagnosticReason(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Op + ": " + pathErr.Err.Error()
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Op + ": " + linkErr.Err.Error()
	}
	reason, _, _ := strings.Cut(err.Error(), "\n")
	reason = diagnosticURL.ReplaceAllString(reason, "[URL hidden]")
	// HTTP error bodies from the download client can include private details.
	return clientErrorBody.ReplaceAllString(reason, "$1")
}

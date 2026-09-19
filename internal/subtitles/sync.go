package subtitles

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file holds the alass runner and the stdout parser that ports
// `sync-swedish-subs.py`, including the progress/framerate/block evidence the
// attempt report keeps.

// AlassBlock is one "shifted block of N subtitles ... by +/-H:MM:SS.mmm" line.
type AlassBlock struct {
	Count int
	Shift string
}

// AlassReport is the parsed alass output.
type AlassReport struct {
	FPS    string
	Blocks []AlassBlock
	Detail string
	// Raw keeps the cleaned output for the attempt log.
	Raw string
}

var (
	alassANSIRe  = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	alassFPSRe   = regexp.MustCompile(`ratio is ([^\s]+)`)
	alassBlockRe = regexp.MustCompile(`shifted block of (\d+) subtitles .*? by ([+-]?\d+:\d{2}:\d{2}\.\d{3})`)
)

// ParseAlassOutput ports `sync-swedish-subs.py::clean_alass_output` plus the
// FPS and block extraction.
func ParseAlassOutput(output string) AlassReport {
	report := AlassReport{Raw: cleanAlassOutput(output)}
	if match := alassFPSRe.FindStringSubmatch(output); match != nil {
		report.FPS = match[1]
	}
	for _, match := range alassBlockRe.FindAllStringSubmatch(output, -1) {
		count, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		report.Blocks = append(report.Blocks, AlassBlock{Count: count, Shift: match[2]})
	}
	var shifts []string
	for _, block := range report.Blocks {
		shifts = append(shifts, block.Shift)
	}
	detail := "fps=" + orNotReported(report.FPS)
	if len(shifts) > 0 {
		detail += "; shifts=" + strings.Join(shifts, ",")
	}
	report.Detail = detail
	return report
}

func orNotReported(value string) string {
	if strings.TrimSpace(value) == "" {
		return "not-reported"
	}
	return value
}

// cleanAlassOutput strips ANSI escapes and progress lines, deduplicates and caps
// the result, exactly like the Python helper.
func cleanAlassOutput(output string) string {
	cleaned := make([]string, 0, 8)
	seen := map[string]bool{}
	stripped := alassANSIRe.ReplaceAllString(output, "")
	stripped = strings.ReplaceAll(stripped, "\r", "\n")
	for _, line := range strings.Split(stripped, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "%") && strings.Contains(line, "/") && strings.Contains(line, "[") {
			continue
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		cleaned = append(cleaned, line)
	}
	joined := strings.Join(cleaned, " | ")
	if len(joined) > 4000 {
		joined = joined[len(joined)-4000:]
	}
	return joined
}

// SyncOptions configures one alass run.
type SyncOptions struct {
	Binary       string
	NoSplit      bool
	SplitPenalty int
	Timeout      time.Duration
}

// SyncResult carries the alass report for the attempt log.
type SyncResult struct {
	Report AlassReport
}

// Syncer runs alass for one reference/downloaded pair.
type Syncer interface {
	Sync(ctx context.Context, referencePath, inputPath, outputPath string, opts SyncOptions) (SyncResult, error)
	Version(ctx context.Context) (string, error)
}

// ExecSyncer runs the configured alass binary as a subprocess. alass is GPL-3.0
// and is deliberately never linked into CineRoute.
type ExecSyncer struct {
	Binary string
}

func (s ExecSyncer) binary() string {
	if strings.TrimSpace(s.Binary) == "" {
		return "alass"
	}
	return s.Binary
}

// Sync runs `alass <reference> <input> <output>`. Both inputs are normalized
// UTF-8 SRT files, matching the proven pipeline.
func (ExecSyncer) Sync(ctx context.Context, referencePath, inputPath, outputPath string, opts SyncOptions) (SyncResult, error) {
	binary := strings.TrimSpace(opts.Binary)
	if binary == "" {
		binary = "alass"
	}
	args := make([]string, 0, 6)
	if opts.NoSplit {
		args = append(args, "--no-split")
	}
	if opts.SplitPenalty > 0 {
		args = append(args, "--split-penalty", strconv.Itoa(opts.SplitPenalty))
	}
	args = append(args, referencePath, inputPath, outputPath)

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, binary, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()
	report := ParseAlassOutput(combined.String())
	if runCtx.Err() == context.DeadlineExceeded {
		return SyncResult{Report: report}, fmt.Errorf("alass timed out after %s", timeout)
	}
	if runErr != nil {
		return SyncResult{Report: report}, fmt.Errorf("alass failed: %s", firstNonEmpty(report.Detail, runErr.Error()))
	}
	return SyncResult{Report: report}, nil
}

// Version runs `alass --version` for the status endpoint.
func (s ExecSyncer) Version(ctx context.Context) (string, error) {
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(versionCtx, s.binary(), "--version")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("alass not available")
	}
	return strings.TrimSpace(strings.SplitN(output.String(), "\n", 2)[0]), nil
}

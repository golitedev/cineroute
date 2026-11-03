package lyrics

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Resolve the exact imported release. Never scan the whole library, a sibling
// release, or a symlink outside the configured mount.
func albumPath(library, rel string) (string, error) {
	if rel == "" || rel == "." || filepath.IsAbs(rel) {
		return "", errors.New("invalid lyrics album path")
	}
	root, err := filepath.EvalSymlinks(library)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	sub, err := filepath.Rel(root, path)
	if err != nil || sub == "." || sub == ".." || strings.HasPrefix(sub, ".."+string(filepath.Separator)) || filepath.IsAbs(sub) {
		return "", errors.New("lyrics album is outside library")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("lyrics album is not a directory")
	}
	return path, nil
}

func (m *Manager) task(id, rel string) Task {
	hash := sha256.Sum256([]byte(id))
	name := hex.EncodeToString(hash[:])
	dir := filepath.Join(m.opts.Data, "lyrics")
	return Task{ID: id, Path: rel, Report: filepath.Join(dir, name+".jsonl"), Log: filepath.Join(dir, name+".log")}
}

func (m *Manager) execute(ctx context.Context, task Task) (Summary, error) {
	path, err := albumPath(m.opts.Library, task.Path)
	if err != nil {
		return Summary{}, err
	}
	args := []string{m.opts.Script, "download", "--root", path, "--all", "--engine", m.opts.Engine, "--report", task.Report, "--outage-wait", "0"}
	if m.opts.VerifiedOnly {
		args = append(args, "--verified-only")
	}
	if m.opts.SyncedOnly {
		args = append(args, "--synced-only")
	}
	if m.opts.RequireAlbumMatch {
		args = append(args, "--require-album-match")
	}
	log, err := os.OpenFile(task.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return Summary{}, err
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, "python3", args...)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the Python runner and its native helper together, including when a
	// shutdown arrives during a provider request. Completed atomic writes survive.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	started := time.Now()
	runErr := cmd.Run()
	summary, complete, reportErr := readReport(task.Report, started)
	if ctx.Err() != nil {
		return summary, ctx.Err()
	}
	if reportErr != nil {
		return summary, reportErr
	}
	if !complete {
		return summary, errors.New("lyrics pass incomplete; see private job log")
	}
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) || exit.ExitCode() != 1 {
			return summary, fmt.Errorf("lyrics process: %w", runErr)
		}
		if summary.Deferred == 0 && summary.Errors == 0 {
			return summary, errors.New("lyrics process failed; see private job log")
		}
	}
	return summary, nil
}

// Summarize the latest result for each track across retries, retaining saved
// counts when subsequent runs skip existing sidecars. Require an end record
// from THIS invocation: an old successful report must not mask a failed launch.
func readReport(path string, started time.Time) (Summary, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, false, err
	}
	defer f.Close()
	tracks := map[string]string{}
	complete := false
	current := ""
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	for scanner.Scan() {
		var row struct {
			Tool         string  `json:"tool"`
			Event        string  `json:"event"`
			RunID        string  `json:"run_id"`
			Path         string  `json:"path"`
			Status       string  `json:"status"`
			Time         float64 `json:"time"`
			PassComplete bool    `json:"pass_complete"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			continue
		}
		if row.Tool != "lrcscript" {
			return Summary{}, false, errors.New("invalid lyrics report")
		}
		if row.Event == "start" && row.Time >= float64(started.UnixNano())/1e9 {
			current = row.RunID
			complete = false
		}
		if row.Event == "track" && row.Path != "" {
			tracks[row.Path] = row.Status
		}
		if row.Event == "end" && current != "" && row.RunID == current {
			complete = row.PassComplete
		}
	}
	if err := scanner.Err(); err != nil {
		return Summary{}, false, err
	}
	var s Summary
	for _, status := range tracks {
		switch status {
		case "saved":
			s.Saved++
		case "missing":
			s.Missing++
		case "deferred":
			s.Deferred++
		case "error":
			s.Errors++
		case "skipped":
			s.Skipped++
		}
	}
	return s, complete, nil
}

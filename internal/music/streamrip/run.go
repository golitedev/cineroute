package streamrip

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// RunOptions describes one `rip url` run.
type RunOptions struct {
	Command    string
	ConfigPath string
	Folder     string // staging folder for this attempt
	Quality    int
	URL        string
	LogPath    string // full output is appended here
	Timeout    time.Duration
	// Secrets are masked in everything CineRoute logs or shows. streamrip
	// prints the token when a login fails.
	Secrets []string
}

// Failure classifies an error streamrip reported in its output.
type Failure int

const (
	FailureNone Failure = iota
	FailureAuth
	FailureIneligible
	FailureAppID
	FailureRateLimited
	FailureDiskFull
	FailureMissingCredentials
	FailureCrash
)

// Fatal reports whether retrying the same album cannot help.
func (f Failure) Fatal() bool {
	switch f {
	case FailureAuth, FailureIneligible, FailureDiskFull, FailureMissingCredentials:
		return true
	}
	return false
}

// Message is a short explanation for the UI.
func (f Failure) Message() string {
	switch f {
	case FailureAuth:
		return "Qobuz rejected the login. Check music.qobuz.user_id and music.qobuz.token in config.yaml (tokens expire when you log out of the web player)."
	case FailureIneligible:
		return "This Qobuz account can't download music (free or lapsed subscription)."
	case FailureAppID:
		return "Qobuz rejected the app id; it was reset and will be fetched again."
	case FailureRateLimited:
		return "Qobuz is rate limiting requests."
	case FailureDiskFull:
		return "The staging disk is full."
	case FailureMissingCredentials:
		return "Qobuz credentials are missing. Set music.qobuz.user_id and music.qobuz.token in config.yaml."
	case FailureCrash:
		return "streamrip crashed."
	}
	return ""
}

// Result is the outcome of one run.
type Result struct {
	ExitCode int
	// Errors are the error lines streamrip logged, in order.
	Errors  []string
	Failure Failure
	// Tail is the end of the output, for the job log in the UI.
	Tail     string
	TimedOut bool
}

// Run executes rip and waits for it. Cancelling ctx stops rip and every
// process it started. The returned error is only for failures to start rip.
func Run(ctx context.Context, o RunOptions) (Result, error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	args := []string{
		"--config-path", o.ConfigPath,
		"--no-db",
		"--no-progress",
		"--folder", o.Folder,
		"--quality", strconv.Itoa(o.Quality),
		"url", o.URL,
	}
	cmd := exec.Command(o.Command, args...)
	cmd.Env = childEnv()
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var logFile io.Writer = io.Discard
	if o.LogPath != "" {
		f, err := os.OpenFile(o.LogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err == nil {
			defer f.Close()
			fmt.Fprintf(f, "\n=== %s rip %s\n", time.Now().Format(time.RFC3339), redact(strings.Join(args, " "), o.Secrets))
			logFile = f
		}
	}

	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return Result{}, fmt.Errorf("starting %s: %w", o.Command, err)
	}

	collector := newCollector()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := redact(sc.Text(), o.Secrets)
			fmt.Fprintln(logFile, line)
			collector.add(line)
		}
		io.Copy(io.Discard, pr)
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		killGroup(cmd, syscall.SIGTERM)
		select {
		case waitErr = <-done:
		case <-time.After(10 * time.Second):
			killGroup(cmd, syscall.SIGKILL)
			waitErr = <-done
		}
	}
	// Children that kept the pipe open must not block us.
	killGroup(cmd, syscall.SIGKILL)
	pw.Close()
	wg.Wait()

	res := collector.result()
	res.TimedOut = timedOut
	res.ExitCode = 0
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		res.ExitCode = ee.ExitCode()
	} else if waitErr != nil {
		res.ExitCode = -1
	}
	if res.Failure == FailureNone && res.ExitCode != 0 && ctx.Err() == nil && res.hasTraceback {
		res.Failure = FailureCrash
	}
	if timedOut {
		fmt.Fprintf(logFile, "=== timed out after %s\n", o.Timeout)
	}
	return res.Result, nil
}

func redact(line string, secrets []string) string {
	for _, s := range secrets {
		if len(s) >= 4 {
			line = strings.ReplaceAll(line, s, "***")
		}
	}
	return line
}

func killGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

// --- output parsing ---------------------------------------------------------

var (
	// Rich log lines look like "[12:00:00] ERROR    message   file.py:123".
	richPrefix = regexp.MustCompile(`^\s*(?:\[\d{1,2}:\d{2}:\d{2}\]\s*)?(DEBUG|INFO|WARNING|ERROR|CRITICAL)\s+`)
	richSuffix = regexp.MustCompile(`\s+[\w.]+\.py:\d+\s*$`)
)

type collector struct {
	mu           sync.Mutex
	errors       []string
	tail         []string
	failure      Failure
	hasTraceback bool
	lastLevel    string
}

type collected struct {
	Result
	hasTraceback bool
}

func newCollector() *collector { return &collector{} }

const tailLines = 60

func (c *collector) add(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line = strings.TrimRight(line, " \t\r")
	if line == "" {
		return
	}
	c.tail = append(c.tail, line)
	if len(c.tail) > tailLines {
		c.tail = c.tail[len(c.tail)-tailLines:]
	}
	low := strings.ToLower(line)
	if strings.Contains(low, "traceback (most recent call last)") {
		c.hasTraceback = true
	}
	if f := classify(low); f != FailureNone && (c.failure == FailureNone || f.Fatal()) {
		c.failure = f
	}

	if m := richPrefix.FindStringSubmatch(line); m != nil {
		c.lastLevel = m[1]
		if m[1] == "ERROR" || m[1] == "CRITICAL" {
			msg := richSuffix.ReplaceAllString(line[len(m[0]):], "")
			c.errors = append(c.errors, strings.TrimSpace(msg))
		}
		return
	}
	// Rich indents continuation lines of a long message.
	if c.lastLevel == "ERROR" && strings.HasPrefix(line, " ") && len(c.errors) > 0 {
		cont := strings.TrimSpace(richSuffix.ReplaceAllString(line, ""))
		if cont != "" && len(c.errors[len(c.errors)-1]) < 500 {
			c.errors[len(c.errors)-1] += " " + cont
		}
	}
}

func (c *collector) result() collected {
	c.mu.Lock()
	defer c.mu.Unlock()
	return collected{
		Result: Result{
			Errors:  append([]string(nil), c.errors...),
			Failure: c.failure,
			Tail:    strings.Join(c.tail, "\n"),
		},
		hasTraceback: c.hasTraceback,
	}
}

// classify recognises well known failures in a lower-cased output line. The
// markers follow what streamrip and Qobuz actually print.
func classify(low string) Failure {
	switch {
	case strings.Contains(low, "missingcredentialserror"):
		return FailureMissingCredentials
	case strings.Contains(low, "ineligibleerror") || strings.Contains(low, "free accounts are not eligible"):
		return FailureIneligible
	case strings.Contains(low, "invalidappiderror") || strings.Contains(low, "invalidappsecreterror") || strings.Contains(low, "invalid app id"):
		return FailureAppID
	case strings.Contains(low, "authenticationerror") || strings.Contains(low, "invalid credentials") ||
		strings.Contains(low, "user authentication failed") || strings.Contains(low, "http 401"):
		return FailureAuth
	case strings.Contains(low, "no space left on device") || strings.Contains(low, "errno 28") ||
		strings.Contains(low, "disk quota exceeded"):
		return FailureDiskFull
	case strings.Contains(low, "http 429") || strings.Contains(low, "too many requests") ||
		strings.Contains(low, "rate limit"):
		return FailureRateLimited
	}
	return FailureNone
}

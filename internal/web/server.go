// Package web serves the CineRoute HTTP interface and runs the intake
// pipeline: parse, classify, TMDB match, destination selection, and the
// stopped-add / verify / start transaction against qBittorrent.
package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cineroute/internal/allocator"
	"cineroute/internal/config"
	"cineroute/internal/copies"
	"cineroute/internal/jellyfin"
	"cineroute/internal/library"
	"cineroute/internal/music"
	"cineroute/internal/prowlarr"
	"cineroute/internal/qbittorrent"
	"cineroute/internal/requests"
	"cineroute/internal/subtitles"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

//go:embed templates
//go:embed logo
var assetsFS embed.FS

const intakeTTL = 2 * time.Hour

const sessionCookie = "cineroute_admin_session"
const publicSessionCookie = "cineroute_user_session"
const sessionTTL = 90 * 24 * time.Hour
const publicSessionTTL = 90 * 24 * time.Hour

// sessionToken signs a legacy expiry with a derived server secret. The caller
// binds that secret to the persistent session key and admin credentials.
func sessionToken(pass string, expires time.Time) string {
	mac := hmac.New(sha256.New, []byte(pass))
	fmt.Fprintf(mac, "cineroute-session:%d", expires.Unix())
	return fmt.Sprintf("%d.%s", expires.Unix(), base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

// validSession checks the expiry and recomputes the HMAC signature.
func validSession(pass, token string) bool {
	i := strings.IndexByte(token, '.')
	if i <= 0 {
		return false
	}
	exp, err := strconv.ParseInt(token[:i], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	want := sessionToken(pass, time.Unix(exp, 0))
	return subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

type Intake struct {
	ID          string
	CreatedAt   time.Time
	Filename    string
	Bytes       []byte
	Meta        *torrentmeta.MetaInfo
	Class       classifierResult
	TMDBResults []tmdb.Result
	TMDBError   string
	Match       *tmdb.Result
	Library     string
	Remote      bool
	Dest        *Destination
	Status      string
	Error       string
	Result      *SubmitResult
	SearchQuery string
	SearchYear  int
}

type classifierResult struct {
	MediaType  string
	Title      string
	AltTitle   string
	Year       int
	Season     int
	Confidence string
}

type Destination struct {
	DriveID          string   `json:"drive_id"`
	DriveName        string   `json:"drive_name"`
	SavePath         string   `json:"save_path"`
	FolderName       string   `json:"folder_name"`
	Library          string   `json:"library"`
	Remote           bool     `json:"remote"`
	Existing         bool     `json:"existing"`
	ExistingAt       string   `json:"existing_at,omitempty"`
	ExistingConflict bool     `json:"existing_conflict,omitempty"`
	ExistingPaths    []string `json:"existing_paths,omitempty"`
	ContentPath      string   `json:"content_path"`
	RootFolder       bool     `json:"root_folder"`
	UsableSpace      int64    `json:"usable_space"`
	NeededBytes      int64    `json:"needed_bytes"`
	EnoughSpace      bool     `json:"enough_space"`
	Shortfall        int64    `json:"shortfall"`
	Warnings         []string `json:"warnings,omitempty"`
}

type SubmitResult struct {
	Hash        string    `json:"hash"`
	TorrentName string    `json:"torrent_name"`
	SavePath    string    `json:"save_path"`
	ContentPath string    `json:"content_path"`
	DriveID     string    `json:"drive_id"`
	RootFolder  bool      `json:"root_folder"`
	Files       int       `json:"files"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type Server struct {
	jellyfin  *jellyfin.Notifier
	music     *music.Service
	accounts  *accounts
	cfg       *config.Config
	prowlarr  *prowlarr.Client
	qb        *qbittorrent.Client
	tmdb      *tmdb.Client
	alloc     *allocator.Allocator
	lib       *library.Scan
	movies    *copies.Manager
	tv        *copies.Manager
	hardlinks *copies.HardlinkManager
	subtitles *subtitles.Manager
	requests  *requests.Store
	engine    *requests.Engine
	allocMu   sync.Mutex
	page      *template.Template

	mu      sync.RWMutex
	intakes map[string]*Intake
	recent  []*Intake
}

func New(cfg *config.Config, qb *qbittorrent.Client, tmdbClient *tmdb.Client, prowlarrClients ...*prowlarr.Client) *Server {
	drives := make([]library.Drive, 0, len(cfg.Drives))
	for _, d := range cfg.Drives {
		drives = append(drives, library.Drive{
			ID:              d.ID,
			MovieRoot:       d.MovieRoot,
			MovieRemoteRoot: d.MovieRemoteRoot,
			TVRoot:          d.TVRoot,
			TVRemoteRoot:    d.TVRemoteRoot,
			AnimeRoot:       d.AnimeRoot,
		})
	}
	var prowlarrClient *prowlarr.Client
	if len(prowlarrClients) > 0 {
		prowlarrClient = prowlarrClients[0]
	}
	scan := library.NewScan(drives)
	s := &Server{
		cfg:       cfg,
		accounts:  newAccounts(cfg),
		qb:        qb,
		prowlarr:  prowlarrClient,
		tmdb:      tmdbClient,
		alloc:     allocator.New(),
		lib:       scan,
		movies:    copies.NewManager(scan, "movie"),
		tv:        copies.NewManager(scan, "tv"),
		hardlinks: copies.NewHardlinkManager(scan),
		subtitles: subtitles.NewManager(subtitleRuntimeConfig(cfg), scan),
		requests:  requests.NewStore(cfg.Requests.StatePath),
		intakes:   map[string]*Intake{},
	}
	s.engine = requests.NewEngine(s.requests, tmdbClient, prowlarrClient, requestBackend{s})
	s.page = template.Must(template.New("index.html").ParseFS(assetsFS, "templates/index.html"))
	go s.cleanupLoop()
	return s
}

func (s *Server) cleanupLoop() {
	for {
		time.Sleep(10 * time.Minute)
		s.mu.Lock()
		cutoff := time.Now().Add(-intakeTTL)
		for id, in := range s.intakes {
			if in.CreatedAt.Before(cutoff) {
				in.Bytes = nil
				delete(s.intakes, id)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.pageIndex)
	mux.HandleFunc("GET /favicon.png", s.serveAsset("logo/favicon.png"))
	mux.HandleFunc("GET /favicon.svg", s.serveAsset("logo/favicon.svg"))
	mux.HandleFunc("GET /logo.svg", s.serveAsset("logo/logo.svg"))
	mux.HandleFunc("GET /requests.js", s.serveAsset("templates/requests.js"))
	mux.HandleFunc("GET /catalog.js", s.serveAsset("templates/catalog.js"))
	mux.HandleFunc("GET /viewport.js", s.serveAsset("templates/viewport.js"))
	mux.HandleFunc("GET /app.css", s.serveAsset("templates/app.css"))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/history", s.historyHandler)
	mux.HandleFunc("GET /api/intakes", s.listIntakes)
	mux.HandleFunc("POST /api/intakes", s.upload)
	mux.HandleFunc("POST /api/intakes/{id}/type", s.setType)
	mux.HandleFunc("DELETE /api/intakes/{id}", s.deleteIntake)
	mux.HandleFunc("POST /api/intakes/{id}/search", s.search)
	mux.HandleFunc("POST /api/intakes/{id}/match", s.match)
	mux.HandleFunc("POST /api/intakes/{id}/destination", s.setDestinationMode)
	mux.HandleFunc("POST /api/intakes/{id}/submit", s.submit)
	mux.HandleFunc("GET /api/copies", s.listCopies)
	mux.HandleFunc("GET /api/catalog/search", s.catalogSearch)
	mux.HandleFunc("GET /api/catalog/{type}/{id}", s.catalogDetails)
	mux.HandleFunc("GET /api/user/requests", s.userRequests)
	mux.HandleFunc("POST /api/user/requests", s.createUserRequest)
	mux.HandleFunc("POST /api/user/requests/{id}/cancel", s.cancelUserRequest)
	mux.HandleFunc("GET /api/requests/queue", s.requestQueue)
	mux.HandleFunc("GET /api/requests/history", s.requestHistory)
	mux.HandleFunc("POST /api/requests/queue/{id}/{action}", s.requestAction)
	mux.HandleFunc("GET /api/requests/settings", s.requestSettings)
	mux.HandleFunc("PUT /api/requests/settings", s.saveRequestSettings)
	mux.HandleFunc("GET /api/requests/indexers", s.requestIndexers)
	mux.HandleFunc("POST /api/copies/scan", s.scanCopies)
	mux.HandleFunc("POST /api/copies/{id}/hardlink", s.hardlinkCopy)
	mux.HandleFunc("GET /api/hardlinks", s.listHardlinks)
	mux.HandleFunc("POST /api/hardlinks/scan", s.scanHardlinks)
	mux.HandleFunc("DELETE /api/hardlinks/{id}", s.removeHardlink)
	mux.HandleFunc("POST /api/hardlinks/{id}/relink", s.relinkHardlink)
	mux.HandleFunc("GET /api/subtitles", s.listSubtitles)
	mux.HandleFunc("PATCH /api/subtitles/settings", s.updateSubtitleSettings)
	mux.HandleFunc("POST /api/subtitles/scan", s.scanSubtitles)
	mux.HandleFunc("POST /api/subtitles/scan/cancel", s.cancelSubtitleJob)
	mux.HandleFunc("POST /api/subtitles/run", s.runSubtitles)
	mux.HandleFunc("POST /api/subtitles/run/cancel", s.cancelSubtitleJob)
	mux.HandleFunc("POST /api/subtitles/work/clear", s.clearSubtitleWork)
	mux.HandleFunc("POST /api/subtitles/{id}/run", s.runSubtitleItem)
	mux.HandleFunc("POST /api/subtitles/{id}/retry", s.retrySubtitleItem)
	mux.HandleFunc("POST /api/subtitles/{id}/skip", s.skipSubtitleItem)
	mux.HandleFunc("POST /api/subtitles/{id}/reset", s.resetSubtitleItem)
	return s.logRequests(s.listenerBoundary(adminListener, s.auth(mux)))
}

// statusRecorder captures the response status and size for the request log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	written, err := r.ResponseWriter.Write(data)
	r.bytes += written
	return written, err
}

// logRequests writes one line per request (method, path, status, size and
// duration) so the server log shows what the browser actually asked for. Health
// checks and the static logo assets are skipped to keep the log readable.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/favicon.png", "/favicon.svg", "/logo.svg":
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"bytes", recorder.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if query := r.URL.RawQuery; query != "" {
			attrs = append(attrs, "query", query)
		}
		if recorder.status >= 400 {
			slog.Warn("http request", attrs...)
			return
		}
		slog.Info("http request", attrs...)
	})
}

// serveAsset serves an embedded static file (logo images) with a cache
// header, since the assets never change between builds.
func (s *Server) serveAsset(name string) http.HandlerFunc {
	data, err := assetsFS.ReadFile(name)
	if err != nil {
		panic("embedded asset missing: " + name)
	}
	ct := "image/svg+xml"
	if strings.HasSuffix(name, ".png") {
		ct = "image/png"
	}
	if strings.HasSuffix(name, ".css") {
		ct = "text/css; charset=utf-8"
	}
	if strings.HasSuffix(name, ".js") {
		ct = "text/javascript; charset=utf-8"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".css") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Write(data)
	}
}

func (s *Server) getIntake(id string) (*Intake, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	in, ok := s.intakes[id]
	return in, ok
}

// groupKey returns the stack group an intake belongs to. TV intakes of the
// same show (same normalized title and year) are stacked into one group so
// seasons and episode packs get matched and submitted together; every movie
// (and a lone TV intake) forms its own group.
func groupKey(in *Intake) string {
	if in.Class.MediaType != "tv" {
		return "movie:" + in.ID
	}
	var b strings.Builder
	b.WriteString("tv:")
	for _, r := range strings.ToLower(in.Class.Title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	b.WriteByte(':')
	fmt.Fprintf(&b, "%d", in.Class.Year)
	return b.String()
}

// groupMembers returns every intake in the same stack group, oldest first.
func (s *Server) groupMembers(key string) []*Intake {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groupMembersLocked(key)
}

// groupMembersLocked is groupMembers for callers that already hold s.mu
// (read or write).
func (s *Server) groupMembersLocked(key string) []*Intake {
	out := []*Intake{}
	for _, in := range s.intakes {
		if groupKey(in) == key {
			out = append(out, in)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Server) storeIntake(in *Intake) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.intakes) > 500 {
		oldest := time.Now()
		var victim string
		for id, v := range s.intakes {
			if v.CreatedAt.Before(oldest) {
				oldest = v.CreatedAt
				victim = id
			}
		}
		delete(s.intakes, victim)
	}
	s.intakes[in.ID] = in
}

func (s *Server) pushHistory(in *Intake) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recent = append(s.recent, in)
	if len(s.recent) > 100 {
		s.recent = s.recent[len(s.recent)-100:]
	}
}

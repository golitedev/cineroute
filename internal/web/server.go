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
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cineroute/internal/allocator"
	"cineroute/internal/config"
	"cineroute/internal/library"
	"cineroute/internal/qbittorrent"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

//go:embed templates
//go:embed logo
var assetsFS embed.FS

const intakeTTL = 2 * time.Hour

const sessionCookie = "cineroute_session"
const sessionTTL = 90 * 24 * time.Hour

// sessionToken signs an expiry timestamp with the auth password as the key,
// so sessions survive restarts but are invalidated when the password changes.
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
	DriveID       string   `json:"drive_id"`
	DriveName     string   `json:"drive_name"`
	SavePath      string   `json:"save_path"`
	FolderName    string   `json:"folder_name"`
	Existing      bool     `json:"existing"`
	ExistingPaths []string `json:"existing_paths,omitempty"`
	ContentPath   string   `json:"content_path"`
	RootFolder    bool     `json:"root_folder"`
	UsableSpace   int64    `json:"usable_space"`
	NeededBytes   int64    `json:"needed_bytes"`
	EnoughSpace   bool     `json:"enough_space"`
	Shortfall     int64    `json:"shortfall"`
	Warnings      []string `json:"warnings,omitempty"`
}

type SubmitResult struct {
	Hash        string    `json:"hash"`
	TorrentName string    `json:"torrent_name"`
	SavePath    string    `json:"save_path"`
	ContentPath string    `json:"content_path"`
	Category    string    `json:"category"`
	DriveID     string    `json:"drive_id"`
	RootFolder  bool      `json:"root_folder"`
	Files       int       `json:"files"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type Server struct {
	cfg     *config.Config
	qb      *qbittorrent.Client
	tmdb    *tmdb.Client
	alloc   *allocator.Allocator
	lib     *library.Scan
	allocMu sync.Mutex
	page    *template.Template

	mu      sync.RWMutex
	intakes map[string]*Intake
	recent  []*Intake
}

func New(cfg *config.Config, qb *qbittorrent.Client, tmdbClient *tmdb.Client) *Server {
	drives := make([]library.Drive, 0, len(cfg.Drives))
	for _, d := range cfg.Drives {
		drives = append(drives, library.Drive{ID: d.ID, MovieRoot: d.MovieRoot, TVRoot: d.TVRoot})
	}
	s := &Server{
		cfg:     cfg,
		qb:      qb,
		tmdb:    tmdbClient,
		alloc:   allocator.New(),
		lib:     library.NewScan(drives),
		intakes: map[string]*Intake{},
	}
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
	mux.HandleFunc("GET /", s.pageIndex)
	mux.HandleFunc("GET /favicon.png", s.serveAsset("logo/favicon.png"))
	mux.HandleFunc("GET /favicon.svg", s.serveAsset("logo/favicon.svg"))
	mux.HandleFunc("GET /logo.svg", s.serveAsset("logo/logo.svg"))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/history", s.historyHandler)
	mux.HandleFunc("GET /api/intakes", s.listIntakes)
	mux.HandleFunc("POST /api/intakes", s.upload)
	mux.HandleFunc("POST /api/intakes/{id}/type", s.setType)
	mux.HandleFunc("DELETE /api/intakes/{id}", s.deleteIntake)
	mux.HandleFunc("POST /api/intakes/{id}/search", s.search)
	mux.HandleFunc("POST /api/intakes/{id}/match", s.match)
	mux.HandleFunc("POST /api/intakes/{id}/submit", s.submit)
	return s.auth(mux)
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
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	}
}

// auth accepts a valid session cookie or Basic Auth credentials. Health
// checks, favicons and the login endpoint are always public; the login form
// is served as part of the page and only the API is gated.
func (s *Server) auth(next http.Handler) http.Handler {
	pass := s.cfg.AuthPassword
	user := s.cfg.AuthUsername
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || p == "/health" || p == "/favicon.png" || p == "/favicon.svg" || p == "/api/login" {
			next.ServeHTTP(w, r)
			return
		}
		if pass != "" {
			if c, err := r.Cookie(sessionCookie); err == nil && validSession(pass, c.Value) {
				next.ServeHTTP(w, r)
				return
			}
			gotUser, pw, ok := r.BasicAuth()
			if !ok || gotUser != user ||
				subtle.ConstantTimeCompare([]byte(pw), []byte(pass)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="cineroute"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// login authenticates with the configured password and sets a session cookie
// valid for sessionTTL. Logging in while auth is disabled is a no-op.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	pass := s.cfg.AuthPassword
	if pass == "" {
		writeJSON(w, http.StatusOK, apiResponse{})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(pass)) != 1 {
		writeErr(w, http.StatusUnauthorized, "invalid password")
		return
	}
	expires := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sessionToken(pass, expires),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		MaxAge:   int(sessionTTL / time.Second),
	})
	writeJSON(w, http.StatusOK, apiResponse{})
}

// logout clears the session cookie.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, apiResponse{})
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

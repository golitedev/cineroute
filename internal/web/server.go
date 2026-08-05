// Package web serves the CineRoute HTTP interface and runs the intake
// pipeline: parse, classify, TMDB match, destination selection, and the
// stopped-add / verify / start transaction against qBittorrent.
package web

import (
	"crypto/subtle"
	"embed"
	"html/template"
	"net/http"
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

	mu      sync.Mutex
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
		alloc:   allocator.New(qb),
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
				delete(s.intakes, id)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.pageIndex)
	mux.HandleFunc("GET /favicon.svg", s.serveAsset("logo/favicon.svg"))
	mux.HandleFunc("GET /logo.svg", s.serveAsset("logo/logo.svg"))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/history", s.historyHandler)
	mux.HandleFunc("POST /api/intakes", s.upload)
	mux.HandleFunc("POST /api/intakes/{id}/type", s.setType)
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
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	}
}

func (s *Server) auth(next http.Handler) http.Handler {
	pass := s.cfg.AuthPassword
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		if pass != "" {
			user, pw, ok := r.BasicAuth()
			if !ok || user != "cineroute" ||
				subtle.ConstantTimeCompare([]byte(pw), []byte(pass)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="cineroute"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) getIntake(id string) (*Intake, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intakes[id]
	return in, ok
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

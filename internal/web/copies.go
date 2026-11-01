package web

import (
	"net/http"

	"cineroute/internal/copies"
)

func (s *Server) copyManager(r *http.Request) *copies.Manager {
	switch r.URL.Query().Get("type") {
	case "tv":
		return s.tv
	case "anime":
		return s.anime
	default:
		return s.movies
	}
}

func (s *Server) listCopies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.copyManager(r).View(r.URL.Query().Get("open")))
}

func (s *Server) scanCopies(w http.ResponseWriter, r *http.Request) {
	manager := s.copyManager(r)
	if err := manager.Scan(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, manager.View(r.URL.Query().Get("open")))
}

func (s *Server) hardlinkCopy(w http.ResponseWriter, r *http.Request) {
	manager := s.copyManager(r)
	id := r.PathValue("id")
	result, err := manager.Hardlink(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result, "view": manager.View(id)})
}

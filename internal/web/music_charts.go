package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"cineroute/internal/music/charts"
)

func (s *Server) chartPlaylists(w http.ResponseWriter, r *http.Request) {
	type response struct {
		Available bool                  `json:"available"`
		Reason    string                `json:"reason,omitempty"`
		Markets   []charts.Market       `json:"markets"`
		Playlists []charts.PlaylistView `json:"playlists"`
	}
	result := response{Markets: charts.Markets(), Playlists: []charts.PlaylistView{}}
	if s.music == nil || s.music.Charts == nil {
		result.Reason = "Enable music in CineRoute to configure chart playlists."
		writeJSON(w, http.StatusOK, result)
		return
	}
	manager := s.music.Charts
	var err error
	if result.Playlists, err = manager.Views(r.Context()); err != nil {
		chartError(w, err)
		return
	}
	result.Available = manager.Available()
	if !result.Available {
		result.Reason = "Set the Navidrome URL and administrator credentials in music.navidrome."
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) saveChartPlaylist(w http.ResponseWriter, r *http.Request) {
	manager := s.chartManager(w)
	if manager == nil {
		return
	}
	var settings charts.Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&settings); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid playlist settings.")
		return
	}
	if settings.Enabled && !manager.Available() {
		writeErr(w, http.StatusConflict, "Configure Navidrome before enabling a chart playlist.")
		return
	}
	id := r.PathValue("id")
	result, err := manager.Configure(r.Context(), id, settings)
	if err != nil {
		chartError(w, err)
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

func (s *Server) syncChartPlaylist(w http.ResponseWriter, r *http.Request) {
	manager := s.chartManager(w)
	if manager == nil {
		return
	}
	if !manager.Available() {
		writeErr(w, http.StatusConflict, "Configure Navidrome before syncing a chart playlist.")
		return
	}
	if err := manager.SyncNow(r.Context(), r.PathValue("id")); err != nil {
		chartError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

func (s *Server) setChartTrackISRC(w http.ResponseWriter, r *http.Request) {
	manager := s.chartManager(w)
	if manager == nil {
		return
	}
	var body struct {
		ISRC *string `json:"isrc"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.ISRC == nil {
		writeErr(w, http.StatusBadRequest, "Provide an ISRC or an empty string to clear it.")
		return
	}
	if err := manager.SetTrackISRC(r.Context(), r.PathValue("id"), r.PathValue("track"), *body.ISRC); err != nil {
		chartError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"saved": true})
}

func (s *Server) removeChartPlaylist(w http.ResponseWriter, r *http.Request) {
	manager := s.chartManager(w)
	if manager == nil {
		return
	}
	if err := manager.Remove(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, charts.ErrNotFound) {
			chartError(w, err)
		} else {
			slog.Warn("removing chart playlist", "err", err)
			writeErr(w, http.StatusBadGateway, "Could not remove the Navidrome playlist. Check the connection and its owner, then try again.")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

func (s *Server) chartImports(w http.ResponseWriter, r *http.Request) {
	if s.music == nil || s.music.Charts == nil {
		writeJSON(w, http.StatusOK, map[string]any{"imports": []charts.Import{}, "more": false, "offset": 0})
		return
	}
	manager := s.music.Charts
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		var err error
		if offset, err = strconv.Atoi(raw); err != nil || offset < 0 {
			writeErr(w, http.StatusBadRequest, "Invalid import history offset.")
			return
		}
	}
	items, more, err := manager.Imports(r.Context(), offset)
	if err != nil {
		chartError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imports": items, "more": more, "offset": offset})
}

func (s *Server) chartManager(w http.ResponseWriter) *charts.Manager {
	if s.music == nil || s.music.Charts == nil {
		writeErr(w, http.StatusServiceUnavailable, "Music is disabled.")
		return nil
	}
	return s.music.Charts
}

func chartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, charts.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, charts.ErrSettings):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), charts.ErrSettings.Error()+": "))
	case errors.Is(err, charts.ErrPaused), errors.Is(err, charts.ErrStale), errors.Is(err, charts.ErrTrackBusy):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		slog.Error("music chart request", "err", err)
		writeErr(w, http.StatusInternalServerError, "Could not save or load music charts.")
	}
}

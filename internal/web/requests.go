package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/medialanguages"
	"cineroute/internal/requests"
)

func (s *Server) requestSettingsResponse(settings requests.Settings) any {
	drives := append([]config.Drive{}, s.cfg.Drives...)
	for i := range drives {
		drives[i].MovieRemoteRoot, _ = s.lib.MovieRemotePath(drives[i].ID, "")
		drives[i].TVRemoteRoot, _ = s.lib.TVRemotePath(drives[i].ID, "")
	}
	return struct {
		Settings           requests.Settings       `json:"settings"`
		Qualities          []requests.Option       `json:"qualities"`
		Codecs             []requests.Option       `json:"codecs"`
		Languages          []medialanguages.Option `json:"languages"`
		SelectionCriteria  []requests.Option       `json:"selection_criteria"`
		Drives             []config.Drive          `json:"drives"`
		ProwlarrConfigured bool                    `json:"prowlarr_configured"`
		EngineAvailable    bool                    `json:"engine_available"`
	}{settings, requests.QualityOptions, requests.CodecOptions, requests.LanguageOptions, requests.SelectionOptions, drives, s.prowlarr.Configured(), true}
}

func (s *Server) requestSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.requests.Load()
	if err != nil {
		slog.Error("load request settings", "err", err)
		writeErr(w, http.StatusInternalServerError, "Request settings could not be loaded. Check the configured settings database.")
		return
	}
	writeJSON(w, http.StatusOK, s.requestSettingsResponse(settings))
}

func (s *Server) saveRequestSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var settings *requests.Settings
	if err := decoder.Decode(&settings); err != nil || settings == nil {
		writeErr(w, http.StatusBadRequest, "Submit a complete request settings object with recognized fields.")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "Submit exactly one settings object.")
		return
	}
	if len(settings.SelectionOrder) == 0 {
		settings.SelectionOrder = requests.DefaultSelectionOrder()
	}
	if err := settings.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.validateRequestDestinations(*settings); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	previous, loadErr := s.requests.Load()
	previous.MovieAutoSubtitles = settings.MovieAutoSubtitles
	searchPolicyChanged := loadErr != nil || !reflect.DeepEqual(previous, *settings)
	if err := s.requests.Save(*settings); err != nil {
		slog.Error("save request settings", "err", err)
		writeErr(w, http.StatusInternalServerError, "Request settings could not be saved. Check that the settings database is writable.")
		return
	}
	if jobs, err := s.requests.Jobs(""); searchPolicyChanged && err == nil {
		for _, job := range jobs {
			if job.Status != "pending" && job.Status != "cancelled" && job.Status != "declined" && job.Status != "error" && job.Status != "complete" {
				job.NextSearch = time.Now().UTC()
				job.Status = "queued"
				_ = s.requests.Update(job)
			}
		}
	}
	if searchPolicyChanged && s.engine != nil {
		s.engine.Wake()
	}
	writeJSON(w, http.StatusOK, s.requestSettingsResponse(*settings))
}

func (s *Server) validateRequestDestinations(settings requests.Settings) error {
	for _, entry := range []struct {
		kind    string
		profile requests.Profile
	}{{"movie", settings.Movie}, {"tv", settings.TV}} {
		p := entry.profile
		if p.NewTitleDrive == "" {
			continue
		}
		drive, found := s.cfg.DriveByID(p.NewTitleDrive)
		if !found {
			return fmt.Errorf("%s: selected disk is no longer configured", entry.kind)
		}
		root := drive.MovieRoot
		if p.Destination == "remote" {
			root, _ = s.lib.MovieRemotePath(drive.ID, "")
		}
		if entry.kind == "tv" {
			root = drive.TVRoot
			if p.Destination == "remote" {
				root, _ = s.lib.TVRemotePath(drive.ID, "")
			}
		}
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("%s: selected disk has no %s library root", entry.kind, p.Destination)
		}
	}
	return nil
}

func (s *Server) requestIndexers(w http.ResponseWriter, r *http.Request) {
	if !s.prowlarr.Configured() {
		writeErr(w, http.StatusServiceUnavailable, "Configure the Prowlarr URL and API key to load trackers.")
		return
	}
	indexers, err := s.prowlarr.Indexers(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Prowlarr could not be reached or rejected the request. Check its URL and API key.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexers": indexers})
}

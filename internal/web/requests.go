package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"cineroute/internal/config"
	"cineroute/internal/requests"
)

func (s *Server) requestSettingsResponse(settings requests.Settings) any {
	drives := append([]config.Drive{}, s.cfg.Drives...)
	for i := range drives {
		drives[i].MovieRemoteRoot, _ = s.lib.MovieRemotePath(drives[i].ID, "")
		drives[i].TVRemoteRoot, _ = s.lib.TVRemotePath(drives[i].ID, "")
	}
	return struct {
		Settings           requests.Settings `json:"settings"`
		Qualities          []requests.Option `json:"qualities"`
		Codecs             []requests.Option `json:"codecs"`
		Drives             []config.Drive    `json:"drives"`
		ProwlarrConfigured bool              `json:"prowlarr_configured"`
		EngineAvailable    bool              `json:"engine_available"`
	}{settings, requests.QualityOptions, requests.CodecOptions, drives, s.prowlarr.Configured(), false}
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
	if err := settings.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.validateRequestDestinations(*settings); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.requests.Save(*settings); err != nil {
		slog.Error("save request settings", "err", err)
		writeErr(w, http.StatusInternalServerError, "Request settings could not be saved. Check that the settings database is writable.")
		return
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

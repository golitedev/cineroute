package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/prowlarr"
	"cineroute/internal/requests"
)

func requestTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Requests.StatePath = filepath.Join(t.TempDir(), "requests.db")
	cfg.Subtitles.Enabled = false
	cfg.Drives = []config.Drive{{ID: "disk1", MovieRoot: "/disk1/media/movies", MovieRemoteRoot: "/disk1/media/movies-remote", TVRoot: "/disk1/media/tv", TVRemoteRoot: "/disk1/media/tv-remote"}}
	return cfg
}

func requestCall(t *testing.T, handler http.Handler, method, path string, settings any, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if settings != nil {
		var err error
		body, err = json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if authenticated {
		req.SetBasicAuth("cineroute", "admin-secret")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestRequestSettingsAuthenticationValidationAndRestart(t *testing.T) {
	cfg := requestTestConfig(t)
	cfg.AuthPassword = "admin-secret"
	handler := New(cfg, nil, nil).Handler()
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		resp := requestCall(t, handler, method, "/api/requests/settings", requests.Defaults(), false)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", method, resp.Code)
		}
	}
	if resp := requestCall(t, handler, http.MethodGet, "/api/requests/indexers", nil, false); resp.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated indexers: %d", resp.Code)
	}
	settings := requests.Defaults()
	settings.Movie.Destination = "main"
	settings.Movie.NewTitleDrive = "disk1"
	settings.TV.Destination = "remote"
	settings.TV.Keywords = []requests.Keyword{{Pattern: "DUAL", Match: "contains", Mode: "preferred", Score: 20}}
	resp := requestCall(t, handler, http.MethodPut, "/api/requests/settings", settings, true)
	if resp.Code != http.StatusOK {
		t.Fatalf("save: %d %s", resp.Code, resp.Body.String())
	}
	invalid := settings
	invalid.Movie.NewTitleDrive = "missing-disk"
	resp = requestCall(t, handler, http.MethodPut, "/api/requests/settings", invalid, true)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid disk: %d %s", resp.Code, resp.Body.String())
	}
	restarted := New(cfg, nil, nil).Handler()
	resp = requestCall(t, restarted, http.MethodGet, "/api/requests/settings", nil, true)
	var view struct {
		Settings        requests.Settings `json:"settings"`
		EngineAvailable bool              `json:"engine_available"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if resp.Code != http.StatusOK || !reflect.DeepEqual(view.Settings, settings) || !view.EngineAvailable {
		t.Fatalf("restart lost policy or advertised execution: %s", resp.Body.String())
	}
	cfg.Drives[0].TVRemoteRoot = ""
	settings.TV.NewTitleDrive = "disk1"
	resp = requestCall(t, New(cfg, nil, nil).Handler(), http.MethodPut, "/api/requests/settings", settings, true)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("missing remote root: %d %s", resp.Code, resp.Body.String())
	}
	page := requestCall(t, handler, http.MethodGet, "/", nil, false).Body.String()
	if !(strings.Index(page, `id="tvTab"`) < strings.Index(page, `id="requestsTab"`) && strings.Index(page, `id="requestsTab"`) < strings.Index(page, `id="hardlinksTab"`)) {
		t.Fatal("Requests tab is not immediately after TV")
	}
	asset := requestCall(t, handler, http.MethodGet, "/requests.js", nil, false)
	if asset.Code != http.StatusOK || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") || asset.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("request script unavailable at login: %d", asset.Code)
	}
}

func TestRequestTrackersOnlyReadIndexerConfiguration(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/indexer" || r.Header.Get("X-Api-Key") != "private-key" {
			t.Errorf("unexpected Prowlarr call: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `[{"id":3,"name":"Torrent tracker","enable":true,"protocol":"torrent"},{"id":4,"name":"Usenet","enable":true,"protocol":"usenet"},{"id":5,"name":"Disabled tracker","enable":false,"protocol":"torrent"}]`)
	}))
	defer upstream.Close()
	cfg := requestTestConfig(t)
	handler := New(cfg, nil, nil, prowlarr.New(upstream.URL, "private-key", time.Second)).Handler()
	resp := requestCall(t, handler, http.MethodGet, "/api/requests/settings", nil, false)
	if resp.Code != http.StatusOK || calls.Load() != 0 {
		t.Fatal("loading settings contacted indexers")
	}
	resp = requestCall(t, handler, http.MethodGet, "/api/requests/indexers", nil, false)
	if resp.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(resp.Body.String(), `"protocol":"torrent"`) || strings.Contains(resp.Body.String(), "private-key") {
		t.Fatalf("indexer response: %d %s", resp.Code, resp.Body.String())
	}
	withoutProwlarr := New(cfg, nil, nil).Handler()
	if resp := requestCall(t, withoutProwlarr, http.MethodGet, "/api/requests/indexers", nil, false); resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured Prowlarr: %d", resp.Code)
	}
}

func TestRequestSettingsRejectMalformedUpdates(t *testing.T) {
	cfg := requestTestConfig(t)
	handler := New(cfg, nil, nil).Handler()
	settings := requests.Defaults()
	if resp := requestCall(t, handler, http.MethodPut, "/api/requests/settings", settings, false); resp.Code != http.StatusOK {
		t.Fatalf("initial save: %s", resp.Body.String())
	}
	payload, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"null", "{}", string(payload) + "{}", strings.TrimSuffix(string(payload), "}") + `,"unknown_field":true}`} {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPut, "/api/requests/settings", strings.NewReader(body)))
		if resp.Code != http.StatusBadRequest {
			t.Errorf("invalid update returned %d: %s", resp.Code, resp.Body.String())
		}
	}
	loaded, err := requests.NewStore(cfg.Requests.StatePath).Load()
	if err != nil || !reflect.DeepEqual(loaded, settings) {
		t.Fatal("malformed update changed saved settings")
	}
}

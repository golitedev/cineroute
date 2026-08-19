package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cineroute/internal/config"
)

func TestCompanionSearchIntervalSettingsAPI(t *testing.T) {
	cfg := config.Default()
	cfg.Companion.StatePath = t.TempDir() + "/companions.json"
	cfg.Drives = nil
	srv := New(cfg, nil, nil)
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	req, err := http.NewRequest(http.MethodPatch, httpSrv.URL+"/api/companions/settings", strings.NewReader(`{"search_interval_seconds":30,"search_batch_size":12}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", resp.StatusCode)
	}
	var view struct {
		SearchIntervalSeconds int `json:"search_interval_seconds"`
		SearchBatchSize       int `json:"search_batch_size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.SearchIntervalSeconds != 30 {
		t.Fatalf("settings interval = %d, want 30", view.SearchIntervalSeconds)
	}
	if view.SearchBatchSize != 12 {
		t.Fatalf("settings batch size = %d, want 12", view.SearchBatchSize)
	}

	req, err = http.NewRequest(http.MethodPatch, httpSrv.URL+"/api/companions/settings", strings.NewReader(`{"search_interval_seconds":1,"search_batch_size":504}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("custom settings status = %d", resp.StatusCode)
	}
	var custom struct {
		SearchIntervalSeconds int `json:"search_interval_seconds"`
		SearchBatchSize       int `json:"search_batch_size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&custom); err != nil {
		t.Fatal(err)
	}
	if custom.SearchIntervalSeconds != 1 || custom.SearchBatchSize != 504 {
		t.Fatalf("custom settings = %+v, want interval 1 and batch 504", custom)
	}

	req, err = http.NewRequest(http.MethodPatch, httpSrv.URL+"/api/companions/settings", strings.NewReader(`{"search_batch_size":0}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid batch settings status = %d", resp.StatusCode)
	}
}

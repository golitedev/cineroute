package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func metadataProwlarr(t *testing.T, trackerBase string, enabled bool) *Client {
	t.Helper()
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer/7" || r.Header.Get("X-Api-Key") != "prowlarr-secret" || r.Header.Get("Authorization") != "" {
			t.Errorf("invalid config request %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 7, "enable": enabled, "protocol": "torrent", "fields": []map[string]any{{"name": "baseUrl", "value": trackerBase}, {"name": "apikey", "value": "tracker-secret"}, {"name": "freeleech", "value": true}}})
	}))
	return New(server.URL, "prowlarr-secret", time.Second)
}
func TestUnit3dMetadataBatchesUsesSavedKeysAndCaches(t *testing.T) {
	var hits, paces atomic.Int32
	var tracker *httptest.Server
	tracker = newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/api/torrents/filter" || r.Header.Get("Authorization") != "Bearer tracker-secret" || r.Header.Get("X-Api-Key") != "" {
			t.Errorf("wrong native metadata authentication/path")
		}
		if r.URL.Query().Get("name") != "Example 2024" || r.URL.Query().Get("perPage") != "100" || r.URL.Query().Get("free[]") != "100" || strings.Contains(r.URL.RawQuery, "secret") {
			t.Errorf("wrong or sensitive query: %s", r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"attributes": map[string]any{"details_link": tracker.URL + "/torrents/42", "media_info": "Video\nBit rate : 9 096 kb/s\nAudio\nLanguage : Spanish\nText\nLanguage : Swedish\nForced : No", "download_link": "https://private/download?passkey=DO_NOT_PUBLISH"}},
			map[string]any{"attributes": map[string]any{"details_link": tracker.URL + "/torrents/43", "media_info": "Audio\nLanguage : English"}},
		}})
	}))
	c := metadataProwlarr(t, tracker.URL, true)
	releases := []Release{{IndexerID: 7, InfoURL: tracker.URL + "/torrents/42"}, {IndexerID: 7, InfoURL: tracker.URL + "/torrents/43"}}
	before := func() error { paces.Add(1); return nil }
	for i := 0; i < 2; i++ {
		out := c.EnrichLanguageMetadata(context.Background(), releases, "Example 2024", before)
		if out[0].Languages.PreferenceScore([]string{"es"}, []string{"sv"}, "en") != 2 || out[1].Languages.PreferenceScore([]string{"es"}, []string{"sv"}, "en") != 0 {
			t.Fatalf("wrong per-torrent metadata: %+v", out)
		}
	}
	if releases[0].Languages.VideoBitrateBPS != 9096000 {
		t.Fatal("video bitrate was lost during enrichment or caching")
	}
	if hits.Load() != 1 || paces.Load() != 1 {
		t.Fatalf("cache/batch failed: hits %d paces %d", hits.Load(), paces.Load())
	}
	c.languages.unavailableUntil[7] = time.Now().Add(5 * time.Minute)
	if out := c.EnrichLanguageMetadata(context.Background(), releases, "Example 2024", before); out[0].Languages.Status != "reported" || hits.Load() != 1 {
		t.Fatal("failure backoff hid valid cached metadata")
	}
	encoded, _ := json.Marshal(releases)
	for _, secret := range []string{"tracker-secret", "prowlarr-secret", "DO_NOT_PUBLISH", "languages"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("release JSON leaked %s", secret)
		}
	}
}
func TestUnit3dNeverSendsKeysToReleaseOriginsOrRedirects(t *testing.T) {
	var stolen atomic.Int32
	external := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { stolen.Add(1) }))
	tracker := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL+"/steal", http.StatusFound)
	}))
	c := metadataProwlarr(t, tracker.URL, true)
	for _, releaseURL := range []string{external.URL + "/torrents/42", tracker.URL + "/torrents/42?api_key=secret", tracker.URL + "/torrents/../../api", tracker.URL + "/torrents/42"} {
		out := c.EnrichLanguageMetadata(context.Background(), []Release{{IndexerID: 7, InfoURL: releaseURL}}, "Example", nil)
		if len(out[0].Languages.Audio) > 0 || len(out[0].Languages.Subtitles) > 0 {
			t.Fatal("untrusted response supplied languages")
		}
	}
	if stolen.Load() != 0 {
		t.Fatal("credentials sent to untrusted origin or redirected")
	}
	// Prowlarr itself must not redirect its API key while retrieving tracker keys.
	proxy := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL+"/config", http.StatusFound)
	}))
	New(proxy.URL, "prowlarr-secret", time.Second).EnrichLanguageMetadata(context.Background(), []Release{{IndexerID: 7, InfoURL: tracker.URL + "/torrents/42"}}, "Example", nil)
	if stolen.Load() != 0 {
		t.Fatal("Prowlarr configuration request leaked credentials")
	}
}
func TestUnit3dFailuresBackOffAndKeepUnknownNeutral(t *testing.T) {
	var hits atomic.Int32
	tracker := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "sensitive raw upstream message", http.StatusTooManyRequests)
	}))
	c := metadataProwlarr(t, tracker.URL, true)
	for _, query := range []string{"Example", "Another"} {
		out := c.EnrichLanguageMetadata(context.Background(), []Release{{IndexerID: 7, InfoURL: tracker.URL + "/torrents/42"}}, query, nil)
		if out[0].Languages.Status != "unavailable" || out[0].Languages.PreferenceScore([]string{"es"}, []string{"sv"}, "en") != 0 {
			t.Fatalf("failure not neutral: %+v", out)
		}
	}
	if hits.Load() != 1 {
		t.Fatal("failed tracker was repeatedly queried")
	}
	c = metadataProwlarr(t, tracker.URL, false)
	c.EnrichLanguageMetadata(context.Background(), []Release{{IndexerID: 7, InfoURL: tracker.URL + "/torrents/42"}}, "Disabled", nil)
	if hits.Load() != 1 {
		t.Fatal("disabled tracker queried")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.EnrichLanguageMetadata(ctx, []Release{{IndexerID: 7, InfoURL: tracker.URL + "/torrents/42"}}, "Cancelled", func() error { t.Fatal("cancelled request was paced"); return nil })
}
func TestUnit3dRejectsInsecureTrackerAndBadTorrentIdentities(t *testing.T) {
	for _, raw := range []string{"https://tracker/torrents/0", "https://tracker/torrents/42/download", "https://user:pass@tracker/torrents/42", "https://tracker/torrents/%34%32", "https://tracker/torrents/42#fragment", "https://tracker/torrents/999999999999999999999"} {
		if id := unit3dTorrentID(raw); id != "" {
			t.Errorf("accepted untrusted identity %s", raw)
		}
	}
	c := metadataProwlarr(t, "http://tracker.example/", true)
	out := c.EnrichLanguageMetadata(context.Background(), []Release{{IndexerID: 7, InfoURL: "http://tracker.example/torrents/42"}}, "Example", func() error {
		t.Fatal("HTTP public tracker received a credential-bearing request")
		return fmt.Errorf("stopped")
	})
	if out[0].Languages.Status != "unsupported" {
		t.Fatal("insecure tracker not rejected")
	}
}

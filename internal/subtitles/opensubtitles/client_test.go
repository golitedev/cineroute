package opensubtitles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server, *recordedRequests) {
	t.Helper()
	recorder := &recordedRequests{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client := New(Config{
		BaseURL:         server.URL,
		APIKey:          "test-key",
		Username:        "user",
		Password:        "pass",
		UserAgent:       "CineRoute/test",
		RequestInterval: 0,
		RequestTimeout:  5 * time.Second,
	})
	return client, server, recorder
}

type recordedRequests struct {
	mu     sync.Mutex
	paths  []string
	query  []string
	auth   []string
	keys   []string
	agent  []string
	bodies []map[string]any
}

func (r *recordedRequests) record(request *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, request.URL.Path)
	r.query = append(r.query, request.URL.RawQuery)
	r.auth = append(r.auth, request.Header.Get("Authorization"))
	r.keys = append(r.keys, request.Header.Get("Api-Key"))
	r.agent = append(r.agent, request.Header.Get("User-Agent"))
	if request.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err == nil && body != nil {
			r.bodies = append(r.bodies, body)
		}
	}
}

const searchPayload = `{
  "total_pages": 1, "total_count": 1, "page": 1,
  "data": [{
    "id": "123",
    "type": "subtitle",
    "attributes": {
      "language": "sv",
      "download_count": 250,
      "from_trusted": true,
      "ratings": 7.5,
      "release": "Movie.2019.1080p.WEB-DL",
      "feature_details": {"feature_id": 4242, "feature_type": "Movie", "movie_name": "Movie", "year": 2019},
      "files": [{"file_id": "777", "file_name": "Movie.2019.1080p.WEB-DL.sv.srt"}]
    }
  }]
}`

func TestClientLoginSearchAndHeaders(t *testing.T) {
	client, _, recorder := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(`{"token":"jwt-token","status":200,"user":{"allowed_downloads":20,"vip":false}}`))
		case "/subtitles":
			_, _ = w.Write([]byte(searchPayload))
		default:
			http.NotFound(w, r)
		}
	})

	response, err := client.Search(context.Background(), SearchQuery{Query: "Movie", Year: 2019})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("data = %+v", response.Data)
	}
	item := response.Data[0]
	if item.Attributes.FeatureDetails.FeatureID.String() != "4242" {
		t.Errorf("feature id = %q", item.Attributes.FeatureDetails.FeatureID)
	}
	// A string file_id is normalized to an int, matching the tolerant parsing.
	if got := item.Attributes.Files[0].FileID.Int(); got != 777 {
		t.Errorf("file id = %d", got)
	}
	if item.Attributes.Ratings.Float64() != 7.5 {
		t.Errorf("ratings = %v", item.Attributes.Ratings.Float64())
	}
	if len(item.Raw) == 0 {
		t.Error("raw JSON must be preserved for the audit trail")
	}

	// The search must be language/type scoped and carry the credential headers.
	params, err := url.ParseQuery(recorder.query[1])
	if err != nil {
		t.Fatalf("parse search query: %v", err)
	}
	if params.Get("languages") != "sv" || params.Get("type") != "movie" ||
		params.Get("query") != "Movie" || params.Get("year") != "2019" {
		t.Errorf("search params = %v", params)
	}
	if recorder.auth[1] != "Bearer jwt-token" {
		t.Errorf("authorization = %q", recorder.auth[1])
	}
	if recorder.keys[1] != "test-key" || recorder.agent[1] != "CineRoute/test" {
		t.Errorf("headers = key %q agent %q", recorder.keys[1], recorder.agent[1])
	}
}

func TestClientDownloadAndQuota(t *testing.T) {
	client, _, recorder := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(`{"token":"t"}`))
		case "/download":
			_, _ = w.Write([]byte(`{"link":"https://dl.opensubtitles.com/sub/1","file_name":"a.srt","remaining":4,"reset_time":"2026-01-02T00:00:00Z"}`))
		case "/infos/user":
			_, _ = w.Write([]byte(`{"data":{"allowed_downloads":100,"remaining_downloads":37}}`))
		default:
			http.NotFound(w, r)
		}
	})

	download, err := client.Download(context.Background(), 777)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if download.Remaining.Int() != 4 || download.ResetTime == "" {
		t.Errorf("download = %+v", download)
	}
	body := recorder.bodies[len(recorder.bodies)-1]
	if body["file_id"] != float64(777) || body["sub_format"] != "srt" {
		t.Errorf("download body = %+v", body)
	}

	info, err := client.UserInfo(context.Background())
	if err != nil {
		t.Fatalf("UserInfo: %v", err)
	}
	remaining, allowed, known := RemainingFromUserInfo(info)
	if !known || remaining != 37 || allowed != 100 {
		t.Errorf("quota = %d/%d known=%v", remaining, allowed, known)
	}
}

func TestClientErrorClassification(t *testing.T) {
	status := http.StatusUnauthorized
	client, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			_, _ = w.Write([]byte(`{"token":"t"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"nope"}`))
	})

	_, err := client.Search(context.Background(), SearchQuery{Query: "x"})
	if !errors.Is(err, ErrUnauthorized) || !IsHardStop(err) {
		t.Fatalf("401 error = %v", err)
	}

	status = http.StatusNotAcceptable
	_, err = client.Search(context.Background(), SearchQuery{Query: "x"})
	if !errors.Is(err, ErrQuota) || !IsHardStop(err) {
		t.Fatalf("406 error = %v", err)
	}

	status = http.StatusTooManyRequests
	_, err = client.Search(context.Background(), SearchQuery{Query: "x"})
	if !errors.Is(err, ErrRateLimited) || !IsHardStop(err) {
		t.Fatalf("429 error = %v", err)
	}
}

func TestDownloadBytesRejectsForeignHosts(t *testing.T) {
	client := New(Config{BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	if _, err := client.DownloadBytes(context.Background(), "http://dl.opensubtitles.com/x"); err == nil {
		t.Error("http links must be rejected")
	}
	if _, err := client.DownloadBytes(context.Background(), "https://evil.example.com/x"); err == nil {
		t.Error("non-opensubtitles hosts must be rejected")
	}
	if _, err := client.DownloadBytes(context.Background(), "not a url"); err == nil {
		t.Error("invalid URLs must be rejected")
	}
}

func TestClientRequiresAPIKey(t *testing.T) {
	client := New(Config{BaseURL: "http://127.0.0.1:1"})
	if client.Configured() {
		t.Error("a client without an API key is not configured")
	}
	if _, err := client.Search(context.Background(), SearchQuery{Query: "x"}); err == nil {
		t.Error("search without an API key must fail")
	}
}

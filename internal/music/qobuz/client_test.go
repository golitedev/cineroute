package qobuz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("42", "tok", func() string { return "123456789" })
	c.BaseURL = srv.URL + "/"
	return c
}

func TestSearchDetectsLinksAndSendsAuth(t *testing.T) {
	var gotQuery string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-Auth-Token") != "tok" || r.Header.Get("X-App-Id") != "123456789" {
			t.Errorf("missing auth headers: %v", r.Header)
		}
		gotQuery = r.URL.Query().Get("query")
		w.Write([]byte(`{"query":"x","albums":{"items":[]}}`))
	})
	raw, err := c.Search(context.Background(), "https://open.qobuz.com/album/0060253780968", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "0060253780968" {
		t.Fatalf("query %q", gotQuery)
	}
	var out map[string]any
	json.Unmarshal(raw, &out)
	if out["switchTo"] != "albums" {
		t.Fatalf("switchTo %v", out["switchTo"])
	}
	raw, _ = c.Search(context.Background(), "outkast", 10, 0)
	json.Unmarshal(raw, &out)
	if gotQuery != "outkast" || out["switchTo"] != nil {
		t.Fatalf("plain search: %q %v", gotQuery, out["switchTo"])
	}
}

func TestAlbumParsing(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"abc","title":"Aquemini","version":"25th Anniversary Edition",
			"artist":{"name":"Outkast"},"release_date_original":"1998-09-29","label":{"name":"LaFace"},
			"genre":{"name":"Hip-Hop/Rap"},"image":{"small":"s.jpg"},"maximum_bit_depth":24,
			"maximum_sampling_rate":96,"streamable":true,"tracks_count":2,
			"tracks":{"total":2,"items":[
				{"id":1,"title":"Hold On, Be Strong","track_number":1,"media_number":1,"duration":30,"streamable":true},
				{"id":2,"title":"Rosa Parks","version":"Radio Edit","track_number":2,"media_number":1,"duration":324,"streamable":false}]}}`))
	})
	a, err := c.Album(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if a.FullTitle() != "Aquemini (25th Anniversary Edition)" || a.Year() != "1998" || a.MaxBitDepth != 24 {
		t.Fatalf("album %+v", a)
	}
	if len(a.Tracks) != 2 || a.Tracks[1].Title != "Rosa Parks (Radio Edit)" || a.Tracks[1].Streamable || a.Tracks[0].ID != "1" {
		t.Fatalf("tracks %+v", a.Tracks)
	}
}

func TestErrors(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"status":"error","code":401,"message":"User authentication is required"}`))
	})
	_, err := c.AlbumRaw(context.Background(), "x")
	if !IsAuth(err) || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("err %v", err)
	}
	if _, err := c.Login(context.Background()); err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("login err %v", err)
	}
	c.Token = ""
	if _, err := c.Search(context.Background(), "x", 1, 0); err == nil {
		t.Fatal("missing token must fail")
	}
}

func TestLoginEligibility(t *testing.T) {
	params := `{"lossy_streaming":true}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("user_auth_token") != "tok" {
			t.Errorf("token not sent")
		}
		w.Write([]byte(`{"user":{"id":42,"display_name":"me","credential":{"label":"Studio","parameters":` + params + `}}}`))
	})
	acct, err := c.Login(context.Background())
	if err != nil || !acct.CanDownload || acct.ID != "42" {
		t.Fatalf("%+v %v", acct, err)
	}
	params = `null`
	acct, _ = c.Login(context.Background())
	if acct.CanDownload {
		t.Fatal("free account must not be able to download")
	}
}

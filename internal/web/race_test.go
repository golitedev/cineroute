package web

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Submitting mutates intake state (status, dest, result) while other
// requests may be listing intakes at the same time; this must not race.
func TestConcurrentSubmitAndList(t *testing.T) {
	_, _, httpSrv, _ := newTestServer(t)
	out := uploadMany(t, httpSrv, map[string][]byte{
		"s1.torrent": singleFileTorrent("The.Office.S01.1080p.WEB-DL.mkv", 200),
		"s2.torrent": singleFileTorrent("The.Office.S02.1080p.WEB-DL.mkv", 200),
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			req, _ := http.NewRequest("GET", httpSrv.URL+"/api/intakes", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			resp, err := http.Post(httpSrv.URL+"/api/intakes/"+out[0].ID+"/submit",
				"application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}
	}()
	wg.Wait()
}

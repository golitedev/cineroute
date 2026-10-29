package medialanguages

import (
	"encoding/json"
	"testing"
)

func TestReportedVideoBitrateIgnoresOtherTracksAndUnreliableRates(t *testing.T) {
	for _, tt := range []struct {
		name, report string
		want         int64
	}{
		{"Moon LatTeam", "General\nOverall bit rate : 10.4 Mb/s\nVideo\nBit rate : 9 096 kb/s\nNominal bit rate : 10 000 kb/s\nAudio #1\nBit rate : 640 kb/s\nAudio #2\nBit rate : 640 kb/s", 9096000},
		{"Moon SyncTeam", "Video\nBit rate : 4 803 kb/s\nAudio\nBit rate : 640 kb/s", 4803000},
		{"decimal", "Video\nBit rate : 9.096 Mb/s", 9096000},
		{"unicode spaces", "Video\nBit rate : 9\u202f096\u00a0kb/s", 9096000},
		{"JSON string", `{"media":{"track":[{"@type":"General","OverallBitRate":"20000000"},{"@type":"Video","BitRate":"9096000","BitRate_Nominal":"10000000"},{"@type":"Audio","BitRate":"640000"}]}}`, 9096000},
		{"JSON number", `{"media":{"track":[{"@type":"Video","BitRate":4803000}]}}`, 4803000},
		{"nominal only", "Video\nNominal bit rate : 10 000 kb/s\nMaximum bit rate : 20 Mb/s", 0},
		{"overall and audio only", "General\nOverall bit rate : 10.4 Mb/s\nVideo\nAudio\nBit rate : 640 kb/s", 0},
		{"multiple video streams", "Video #1\nBit rate : 9 Mb/s\nVideo #2\nBit rate : 1 Mb/s", 0},
		{"invalid", "Video\nBit rate : unknown", 0},
		{"range", "Video\nBit rate : 4 Mb/s / 10 Mb/s", 0},
		{"negative", "Video\nBit rate : -1 Mb/s", 0},
		{"oversized", "Video\nBit rate : 9999999999999999999999999999999 Mb/s", 0},
		{"ambiguous separator", "Video\nBit rate : 9,096 kb/s", 0},
		{"malformed JSON", `{"media":{"track":[{"@type":"Video","BitRate":true}]}}`, 0},
		{"no bitrate inference from size", "General\nFile size : 7.06 GiB\nDuration : 1 h 37 min", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.report, "")
			if got.VideoBitrateBPS != tt.want {
				t.Fatalf("got %d, want %d", got.VideoBitrateBPS, tt.want)
			}
			if tt.want > 0 && got.Status != "reported" {
				t.Fatal("bitrate-only report was not retained")
			}
		})
	}
}

func TestVideoBitrateSurvivesMetadataPersistence(t *testing.T) {
	m := Parse("Video\nBit rate : 9 096 kb/s\nAudio\nLanguage : English", "")
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var restored Metadata
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.VideoBitrateBPS != 9096000 || len(restored.Audio) != 1 {
		t.Fatalf("metadata lost: %+v", restored)
	}
}

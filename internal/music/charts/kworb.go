// Package charts synchronizes Kworb's Spotify charts with a local music library.
package charts

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type Market struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// Kworb's supported Spotify daily charts, from https://kworb.net/spotify/.
//
//go:embed markets.json
var marketData []byte

var markets = func() []Market {
	var result []Market
	if err := json.Unmarshal(marketData, &result); err != nil {
		panic(err)
	}
	return result
}()

func Markets() []Market { return slices.Clone(markets) }

func marketLabel(code string) string {
	for _, market := range markets {
		if market.Code == code {
			return market.Label
		}
	}
	return ""
}

type Track struct {
	SpotifyID string   `json:"spotify_id"`
	Rank      int      `json:"rank"`
	Title     string   `json:"title"`
	Artists   []string `json:"artists"`
}

type Snapshot struct {
	Date   string  `json:"date"`
	Tracks []Track `json:"tracks"`
}

var (
	chartDate = regexp.MustCompile(`Spotify Daily Chart - .+ - (\d{4}/\d{2}/\d{2})`)
	trackLink = regexp.MustCompile(`^\.\./track/([A-Za-z0-9]{22})\.html$`)
)

func fetchKworb(ctx context.Context, country string) (Snapshot, error) {
	if marketLabel(country) == "" {
		return Snapshot{}, fmt.Errorf("unknown Kworb market")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://kworb.net/spotify/country/"+country+"_daily.html", nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("User-Agent", "CineRoute (daily chart sync)")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("Kworb HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Snapshot{}, err
	}
	return parseKworb(body)
}

func parseKworb(body []byte) (Snapshot, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil || !bytes.Contains(body, []byte("</table>")) {
		return Snapshot{}, fmt.Errorf("invalid Kworb chart page")
	}
	var snapshot Snapshot
	var table *html.Node
	for node := range doc.Descendants() {
		for _, attr := range node.Attr {
			switch {
			case attr.Key == "id" && attr.Val == "spotifydaily" && node.DataAtom == atom.Table:
				table = node
			case attr.Key == "class" && slices.Contains(strings.Fields(attr.Val), "pagetitle"):
				match := chartDate.FindStringSubmatch(nodeText(node))
				if match != nil {
					if date, err := time.Parse("2006/01/02", match[1]); err == nil {
						snapshot.Date = date.Format(time.DateOnly)
					}
				}
			}
		}
	}
	if snapshot.Date == "" || table == nil {
		return Snapshot{}, fmt.Errorf("Kworb chart date or table is missing")
	}

	seen := make(map[string]bool)
	for row := range table.Descendants() {
		if row.DataAtom != atom.Tr || row.Parent.DataAtom != atom.Tbody {
			continue
		}
		cells := make([]*html.Node, 0, 11)
		for cell := range row.ChildNodes() {
			if cell.DataAtom == atom.Td {
				cells = append(cells, cell)
			}
		}
		if len(cells) != 11 {
			return Snapshot{}, fmt.Errorf("unexpected Kworb chart row")
		}
		rank, err := strconv.Atoi(nodeText(cells[0]))
		if err != nil || rank != len(snapshot.Tracks)+1 {
			return Snapshot{}, fmt.Errorf("invalid Kworb chart rank")
		}
		track := Track{Rank: rank}
		for link := range cells[2].Descendants() {
			if link.DataAtom != atom.A {
				continue
			}
			for _, attr := range link.Attr {
				if attr.Key != "href" {
					continue
				}
				if strings.HasPrefix(attr.Val, "../artist/") {
					track.Artists = append(track.Artists, nodeText(link))
				} else if match := trackLink.FindStringSubmatch(attr.Val); match != nil {
					track.SpotifyID, track.Title = match[1], nodeText(link)
				}
			}
		}
		if track.SpotifyID == "" || track.Title == "" || len(track.Artists) == 0 || track.Artists[0] == "" || seen[track.SpotifyID] {
			return Snapshot{}, fmt.Errorf("invalid Kworb chart track at rank %d", rank)
		}
		seen[track.SpotifyID] = true
		snapshot.Tracks = append(snapshot.Tracks, track)
	}
	if len(snapshot.Tracks) == 0 || len(snapshot.Tracks) > 200 {
		return Snapshot{}, fmt.Errorf("unexpected Kworb chart length")
	}
	return snapshot, nil
}

func nodeText(node *html.Node) string {
	var text strings.Builder
	for child := range node.Descendants() {
		if child.Type == html.TextNode {
			text.WriteString(child.Data)
		}
	}
	return strings.TrimSpace(text.String())
}

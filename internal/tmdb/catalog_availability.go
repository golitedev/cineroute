package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type catalogReleaseKey struct {
	kind string
	id   int
}
type catalogReleaseEntry struct {
	available bool
	expires   time.Time
}

// Movies need an actual TMDB digital (type 4) release in any country. A
// theatrical or physical date alone is insufficient. TMDB does not have the
// same release-type dates for TV, so TV visibility uses a reached first-air or
// aired regular-episode date. Unknown dates stay hidden in this strict mode.
func DigitallyAvailable(d Details, now time.Time) bool {
	if d.MediaType == "movie" {
		for _, country := range d.ReleaseDates.Results {
			for _, date := range country.Dates {
				if date.Type == 4 && dateReached(date.Date, now) {
					return true
				}
			}
		}
		return false
	}
	if d.MediaType == "tv" {
		return dateReached(d.FirstAirDate, now) || (d.LastEpisodeToAir != nil && d.LastEpisodeToAir.Season > 0 && dateReached(d.LastEpisodeToAir.AirDate, now))
	}
	return false
}
func dateReached(value string, now time.Time) bool {
	if value == "" {
		return false
	}
	if date, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return !date.After(now)
	}
	date, err := time.Parse("2006-01-02", value)
	return err == nil && !date.After(now.UTC())
}
func (c *Client) cachedCatalogRelease(key catalogReleaseKey) (bool, bool) {
	c.catalogReleaseMu.Lock()
	defer c.catalogReleaseMu.Unlock()
	entry, ok := c.catalogReleases[key]
	return entry.available, ok && time.Now().Before(entry.expires)
}
func (c *Client) catalogReleased(ctx context.Context, item Result) (bool, error) {
	key := catalogReleaseKey{item.MediaType, item.ID}
	if key.id <= 0 || (key.kind != "movie" && key.kind != "tv") {
		return false, fmt.Errorf("invalid catalog identity")
	}
	if available, ok := c.cachedCatalogRelease(key); ok {
		return available, nil
	}
	select {
	case c.catalogReleaseSlots <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-c.catalogReleaseSlots }()
	if available, ok := c.cachedCatalogRelease(key); ok {
		return available, nil
	}
	var details Details
	query := url.Values{}
	if key.kind == "movie" {
		query.Set("append_to_response", "release_dates")
	}
	if err := c.getCatalogJSON(ctx, key.kind+"/"+strconv.Itoa(key.id), query, &details); err != nil {
		return false, err
	}
	if details.ID != key.id {
		return false, fmt.Errorf("invalid TMDB availability response")
	}
	details.MediaType = key.kind
	available := !details.Adult && DigitallyAvailable(details, time.Now())
	c.catalogReleaseMu.Lock()
	if len(c.catalogReleases) >= 1024 {
		for id, entry := range c.catalogReleases {
			if time.Now().After(entry.expires) {
				delete(c.catalogReleases, id)
			}
		}
		if len(c.catalogReleases) >= 1024 {
			for id := range c.catalogReleases {
				delete(c.catalogReleases, id)
				break
			}
		}
	}
	c.catalogReleases[key] = catalogReleaseEntry{available: available, expires: time.Now().Add(15 * time.Minute)}
	c.catalogReleaseMu.Unlock()
	return available, nil
}

// FilterReleasedCatalog preserves TMDB's ordering and page numbers. Empty
// filtered pages may still have later results, so callers retain Load more.
// Four workers plus a client-wide semaphore bound lookup concurrency across
// users; cached checks avoid repeating the same title's release lookup.
func (c *Client) FilterReleasedCatalog(ctx context.Context, page CatalogPage) (CatalogPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if len(page.Results) > 20 {
		page.Results = page.Results[:20]
	}
	visible := make([]bool, len(page.Results))
	work := make(chan int, len(page.Results))
	for i := range page.Results {
		work <- i
	}
	close(work)
	var wg sync.WaitGroup
	var once sync.Once
	var firstError error
	for n := 0; n < min(4, len(page.Results)); n++ {
		wg.Go(func() {
			for i := range work {
				if ctx.Err() != nil {
					return
				}
				available, err := c.catalogReleased(ctx, page.Results[i])
				if err != nil {
					once.Do(func() { firstError = err; cancel() })
					return
				}
				visible[i] = available
			}
		})
	}
	wg.Wait()
	if firstError != nil {
		return CatalogPage{}, firstError
	}
	if ctx.Err() != nil {
		return CatalogPage{}, ctx.Err()
	}
	results := []Result{}
	for i, item := range page.Results {
		if visible[i] {
			results = append(results, item)
		}
	}
	page.Results = results
	page.AvailabilityFiltered = true
	return page, nil
}

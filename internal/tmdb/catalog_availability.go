package tmdb

import "time"

// DigitallyAvailable supplies an advisory signal for the request dialog.
// Missing or future digital dates produce a warning, never a catalog filter.
// TV uses reached first-air or aired regular-episode dates.
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

package requests

import (
	"slices"
	"strings"
	"unicode"

	"cineroute/internal/prowlarr"
)

// User preferences only influence selection; administrators still own filters,
// approval, selected trackers, quality order and library placement.
func requestProfile(profile Profile, job Job) Profile {
	if job.PreferSpanishAudio && !slices.Contains(profile.PreferredAudioLanguages, "es") {
		profile.PreferredAudioLanguages = append(slices.Clone(profile.PreferredAudioLanguages), "es")
	}
	return profile
}

// Recognize the configured Prowlarr definition, with exact normalized display
// names as a fallback for older servers. A release's claimed indexer name cannot
// grant priority or enable a tracker that the administrator did not select.
func isLatTeam(indexer prowlarr.Indexer) bool {
	if indexer.DefinitionName != "" {
		return strings.EqualFold(indexer.DefinitionName, "lat-team-api")
	}
	name := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, indexer.Name)
	return name == "latteam" || name == "latteamapi"
}

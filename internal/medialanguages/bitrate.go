package medialanguages

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var videoBitratePattern = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)([kmg]?(?:b/s|bits/s|bps))?$`)

func parseVideoBitrate(raw string) int64 {
	raw = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)
	match := videoBitratePattern.FindStringSubmatch(raw)
	if match == nil {
		return 0
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(match[2]) {
	case "kb/s", "kbits/s", "kbps":
		value *= 1e3
	case "mb/s", "mbits/s", "mbps":
		value *= 1e6
	case "gb/s", "gbits/s", "gbps":
		value *= 1e9
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 10e9 {
		return 0
	}
	return int64(math.Round(value))
}

// MediaInfo JSON represents bits/second as either a string or a number.
func jsonBitrate(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return string(raw)
}

package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const maxFallbackQueries = 8
const maxAliasChecks = 3

// SearchCatalog preserves TMDB's native results/order and pagination. Only an
// empty first page is broadened; every suggestion still has a real TMDB ID.
func (c *Client) SearchCatalog(ctx context.Context, query, kind string, page int) (CatalogPage, error) {
	exact, err := c.searchCatalogExact(ctx, query, kind, page)
	if err != nil || page != 1 || len(exact.Results) > 0 {
		return exact, err
	}
	queries := catalogFragments(query)
	if len(queries) == 0 {
		return exact, nil
	}
	fallbackCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	candidates := []Result{}
	seen := map[string]bool{}
	successful := false
	var lastError error
	// Small batches keep typical corrections quick without fan-out across the
	// entire catalog. A client-wide semaphore also caps concurrent fallback I/O.
	for start := 0; start < len(queries); start += 3 {
		batch := queries[start:min(start+3, len(queries))]
		type response struct {
			page CatalogPage
			err  error
		}
		replies := make([]chan response, len(batch))
		for i, fragment := range batch {
			replies[i] = make(chan response, 1)
			go func() {
				var out CatalogPage
				err := c.withCatalogSlot(fallbackCtx, func() error {
					var err error
					out, err = c.searchCatalogExact(fallbackCtx, fragment, kind, 1)
					return err
				})
				replies[i] <- response{out, err}
			}()
		}
		// Consume in query order, never completion order, so ties stay deterministic.
		for _, reply := range replies {
			r := <-reply
			if r.err != nil {
				lastError = r.err
				continue
			}
			successful = true
			for _, item := range r.page.Results {
				key := fmt.Sprintf("%s:%d", item.MediaType, item.ID)
				if !seen[key] {
					seen[key] = true
					candidates = append(candidates, item)
				}
			}
		}
		ranked := rankCatalogSuggestions(candidates, query, nil)
		if len(ranked) > 0 && catalogSimilarity(query, ranked[0]) >= 0.92 {
			return similarCatalogPage(ranked), nil
		}
		if fallbackCtx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return CatalogPage{}, ctx.Err()
	}
	aliases := map[string][]string{}
	// A translated/alternative title may bear no resemblance to the displayed
	// English/original name. Check a few leading native hits before rejecting it.
	if len(rankCatalogSuggestions(candidates, query, nil)) == 0 && fallbackCtx.Err() == nil {
		for _, item := range candidates[:min(maxAliasChecks, len(candidates))] {
			var titles []string
			err := c.withCatalogSlot(fallbackCtx, func() error {
				var err error
				titles, err = c.catalogAliases(fallbackCtx, item)
				return err
			})
			if err == nil {
				aliases[catalogKey(item)] = titles
			}
			if fallbackCtx.Err() != nil {
				break
			}
		}
	}
	ranked := rankCatalogSuggestions(candidates, query, aliases)
	if len(ranked) > 0 {
		return similarCatalogPage(ranked), nil
	}
	if !successful && lastError != nil {
		return CatalogPage{}, lastError
	}
	return exact, nil
}

// Translations and alternative titles are separate TMDB datasets. Fetch them
// only for a few otherwise-unmatched suggestions, never for a normal search.
func (c *Client) catalogAliases(ctx context.Context, item Result) ([]string, error) {
	var details struct {
		Details
		Translations struct {
			Items []struct {
				Data struct {
					Title string `json:"title"`
					Name  string `json:"name"`
				} `json:"data"`
			} `json:"translations"`
		} `json:"translations"`
	}
	err := c.getCatalogJSON(ctx, item.MediaType+"/"+strconv.Itoa(item.ID), url.Values{"append_to_response": {"alternative_titles,translations"}}, &details)
	if err != nil {
		return nil, err
	}
	if details.ID != item.ID || details.Adult {
		return nil, fmt.Errorf("invalid catalog identity")
	}
	titles := []string{details.DisplayTitle(), details.OriginalTitle, details.OriginalName}
	for _, translation := range details.Translations.Items {
		titles = append(titles, translation.Data.Title, translation.Data.Name)
	}
	titles = append(titles, details.Aliases()...)
	return titles[:min(128, len(titles))], nil
}

func (c *Client) withCatalogSlot(ctx context.Context, action func() error) error {
	select {
	case c.catalogFallbackSlots <- struct{}{}:
		defer func() { <-c.catalogFallbackSlots }()
		return action()
	case <-ctx.Done():
		return ctx.Err()
	}
}
func similarCatalogPage(items []Result) CatalogPage {
	return CatalogPage{Page: 1, TotalPages: 1, Results: items[:min(20, len(items))], SimilarMatches: true}
}
func catalogKey(item Result) string { return fmt.Sprintf("%s:%d", item.MediaType, item.ID) }
func catalogWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) })
}
func catalogCompact(s string) []rune {
	// Retain non-Latin letters and bound work on upstream titles as well as
	// user queries. Punctuation/spacing never costs an edit.
	chars := make([]rune, 0, min(len(s), 256))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			chars = append(chars, unicode.ToLower(r))
			if len(chars) == 256 {
				break
			}
		}
	}
	return chars
}
func catalogFragments(query string) []string {
	words := catalogWords(query)
	out := []string{}
	seen := map[string]bool{strings.ToLower(strings.TrimSpace(query)): true}
	add := func(fragment string) {
		fragment = strings.TrimSpace(fragment)
		if len([]rune(fragment)) >= 3 && !seen[fragment] && len(out) < maxFallbackQueries {
			seen[fragment] = true
			out = append(out, fragment)
		}
	}
	if len(words) == 0 {
		return out
	}
	// Normalize punctuation/extra spaces only after the untouched native query.
	add(strings.Join(words, " "))
	if len(words) == 1 {
		letters := []rune(words[0])
		if len(letters) < 5 {
			return out
		}
		// Prefixes recover suffix typos; suffixes also recover joined title words,
		// e.g. "thebigshort" -> "short", without a title/spelling dictionary.
		for _, size := range []int{len(letters) * 3 / 4, len(letters) / 2, 6, 5, 4} {
			if size < 3 || size >= len(letters) {
				continue
			}
			add(string(letters[:size]))
			add(string(letters[len(letters)-size:]))
		}
		return out
	}
	if len([]rune(strings.Join(words[:len(words)-1], " "))) >= 4 {
		// Retain the phrase's language/context while relaxing its final word.
		// For example, "el lobo de walstreet" can find "el lobo de wal".
		last := []rune(words[len(words)-1])
		if len(last) > 3 {
			add(strings.Join(words[:len(words)-1], " ") + " " + string(last[:3]))
		}
		add(strings.Join(words[:len(words)-1], " "))
	}
	significant := []string{}
	for _, word := range words {
		if len([]rune(word)) < 3 || slices.Contains([]string{"the", "and", "for", "from", "with", "los", "las", "les", "der", "die", "das"}, word) {
			continue
		}
		numeric := true
		for _, r := range word {
			numeric = numeric && unicode.IsDigit(r)
		}
		if !numeric && !slices.Contains(significant, word) {
			significant = append(significant, word)
		}
	}
	sort.SliceStable(significant, func(i, j int) bool { return len([]rune(significant[i])) > len([]rune(significant[j])) })
	significant = significant[:min(3, len(significant))]
	for _, word := range significant {
		add(word)
	}
	for _, word := range significant {
		letters := []rune(word)
		if len(letters) >= 5 {
			add(string(letters[:max(4, min(6, len(letters)*3/4))]))
		}
	}
	for _, word := range significant {
		letters := []rune(word)
		if len(letters) > 4 {
			add(string(letters[:4]))
		}
	}
	return out
}
func catalogSimilarity(query string, item Result) float64 {
	best := 0.0
	for _, title := range []string{item.DisplayTitle(), item.OriginalTitle, item.OriginalName} {
		best = max(best, titleSimilarity(query, title))
	}
	return best
}
func rankCatalogSuggestions(items []Result, query string, aliases map[string][]string) []Result {
	type scored struct {
		item  Result
		score float64
	}
	candidates := []scored{}
	for _, item := range items {
		if item.ID <= 0 || item.Adult || (item.MediaType != "movie" && item.MediaType != "tv") {
			continue
		}
		score := catalogSimilarity(query, item)
		for _, alias := range aliases[catalogKey(item)] {
			score = max(score, titleSimilarity(query, alias))
		}
		if score >= 0.74 {
			candidates = append(candidates, scored{item, score})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].item.Popularity != candidates[j].item.Popularity {
			return candidates[i].item.Popularity > candidates[j].item.Popularity
		}
		return candidates[i].item.VoteCount > candidates[j].item.VoteCount
	})
	out := make([]Result, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.item)
	}
	return out
}
func titleSimilarity(a, b string) float64 {
	left, right := catalogCompact(a), catalogCompact(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	// Optimal-string-alignment distance also counts a transposed letter pair as
	// one edit. Three short rows bound memory even for long upstream titles.
	prev2 := make([]int, len(right)+1)
	prev := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 0
			if left[i-1] != right[j-1] {
				cost = 1
			}
			current[j] = min(prev[j]+1, current[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && left[i-1] == right[j-2] && left[i-2] == right[j-1] {
				current[j] = min(current[j], prev2[j-2]+1)
			}
		}
		prev2, prev, current = prev, current, prev2
	}
	return 1 - float64(prev[len(right)])/float64(max(len(left), len(right)))
}

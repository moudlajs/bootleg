// Package matcher picks the best catalog search result for a query; it does no I/O.
package matcher

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Candidate is one search result, reduced to what matching needs.
type Candidate struct {
	Artist string
	Title  string
}

// junkPatterns mark cover/karaoke uploads; stored already normalised.
var junkPatterns = []string{"karaoke", "tribute", "made famous by", "in the style of"}

// Scores: an exact artist outweighs any title evidence.
const (
	artistExact   = 4
	artistPartial = 2
	titleExact    = 2
	titlePartial  = 1
)

// Best returns the index of the best candidate, or false; an empty artist makes title a free-form term.
func Best(artist, title string, candidates []Candidate) (int, bool) {
	qArtist, qTitle := Normalize(artist), Normalize(title)
	queryIsJunk := isJunk(qArtist) || isJunk(qTitle)

	best, bestScore := -1, 0
	for i, c := range candidates {
		cArtist, cTitle := Normalize(c.Artist), Normalize(c.Title)
		if !queryIsJunk && (isJunk(cArtist) || isJunk(cTitle)) {
			continue
		}

		var s int
		if qArtist == "" {
			s = scoreTerm(qTitle, cArtist, cTitle)
		} else {
			s = score(qArtist, qTitle, cArtist, cTitle)
		}
		// Strictly greater, so ties keep the API's relevance order.
		if s > bestScore {
			best, bestScore = i, s
		}
	}
	return best, best >= 0
}

// score is 0 unless both artist and title match at least partially.
func score(qArtist, qTitle, cArtist, cTitle string) int {
	var a, t int
	switch {
	case qArtist == cArtist:
		a = artistExact
	case containsWords(cArtist, qArtist) || containsWords(qArtist, cArtist):
		a = artistPartial // "Björk" vs "Björk & Thom Yorke"
	default:
		return 0
	}
	switch {
	case qTitle == cTitle:
		t = titleExact
	case containsWords(cTitle, qTitle) || containsWords(qTitle, cTitle):
		t = titlePartial // "Glory Box" vs "Glory Box (Remastered)"
	default:
		return 0
	}
	return a + t
}

func scoreTerm(term, cArtist, cTitle string) int {
	if cTitle == "" || !containsWords(term, cTitle) {
		return 0
	}
	s := titlePartial
	if cArtist != "" && containsWords(term, cArtist) {
		s += artistPartial
	}
	return s
}

// containsWords matches on word boundaries: "me" matches "army of me" but not "home".
func containsWords(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains(" "+haystack+" ", " "+needle+" ")
}

func isJunk(s string) bool {
	for _, p := range junkPatterns {
		if containsWords(s, p) {
			return true
		}
	}
	return false
}

// foldExtra covers letters that NFD can't split into base letter plus accent.
var foldExtra = strings.NewReplacer(
	"ø", "o", "æ", "ae", "œ", "oe", "ß", "ss", "ł", "l", "đ", "d", "ð", "d", "þ", "th", "ı", "i",
)

// Normalize lowercases, strips diacritics and punctuation: "Björk" and "BJORK!" become "bjork".
func Normalize(s string) string {
	// NFD splits "ö" into "o" + a mark (Mn), which is dropped; a Transformer has state, so one per call.
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, strings.ToLower(s))
	if err != nil {
		// Only possible on invalid UTF-8.
		folded = strings.ToLower(s)
	}
	folded = foldExtra.Replace(folded)

	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, folded)), " ")
}

// Package filter implements adult/spam content filtering for indexed
// torrents. A torrent is dropped when either Adult or Spam is true.
package filter

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// File is a single file entry of a torrent.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Result is the outcome of Check. Reasons explains every signal that hit.
type Result struct {
	Adult   bool     `json:"adult"`
	Spam    bool     `json:"spam"`
	Reasons []string `json:"reasons,omitempty"`
}

const (
	maxNameLen        = 300
	maxTotalSize      = 20 << 40 // 20 TiB
	maxFileCount      = 20000
	stuffingMinRepeat = 4
	nonAlnumMaxRatio  = 0.5
	randomMinLen      = 20
	randomMaxRatio    = 0.3
	randomMinFiles    = 5
)

var (
	adultDomainRe = regexp.MustCompile(adultDomainPattern)
	javCodeRe     = regexp.MustCompile(javCodePattern)

	// substrings holds tokens matched by plain substring search.
	substrings []string
	// wordRe matches short ASCII tokens on word boundaries.
	wordRe *regexp.Regexp
)

func init() {
	var shorts []string
	for _, kw := range adultKeywords {
		kw = strings.ToLower(kw)
		if isShortASCII(kw) {
			shorts = append(shorts, regexp.QuoteMeta(kw))
		} else {
			substrings = append(substrings, kw)
		}
	}
	if len(shorts) > 0 {
		wordRe = regexp.MustCompile(`\b(?:` + strings.Join(shorts, "|") + `)\b`)
	}
}

// isShortASCII reports whether s is a short pure-ASCII token for which
// substring matching would cause too many false positives.
func isShortASCII(s string) bool {
	if len(s) == 0 || len(s) > 4 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// Check inspects a torrent's name, file list and total size and returns
// whether it should be dropped as adult or spam content.
func Check(name string, files []File, totalSize int64) Result {
	var r Result
	add := func(format string, args ...interface{}) {
		r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...))
	}

	lowerName := strings.ToLower(name)

	// --- Adult signals ---
	adultHit := false
	checkAdult := func(what, text string) {
		if text == "" {
			return
		}
		for _, kw := range substrings {
			if strings.Contains(text, kw) {
				add("adult keyword %q in %s", kw, what)
				adultHit = true
				return
			}
		}
		if wordRe != nil {
			if m := wordRe.FindString(text); m != "" {
				add("adult token %q in %s", m, what)
				adultHit = true
				return
			}
		}
		if m := adultDomainRe.FindString(text); m != "" {
			add("adult site domain %q in %s", m, what)
			adultHit = true
		}
	}
	checkAdult("name", lowerName)
	for _, f := range files {
		checkAdult("file", strings.ToLower(f.Path))
	}
	// JAV code is a weak signal: only flag when another adult signal hit.
	if javCodeRe.MatchString(name) && adultHit {
		add("jav-style product code in name")
	}
	r.Adult = adultHit

	// --- Spam signals ---
	if strings.TrimSpace(name) == "" {
		add("empty name")
		r.Spam = true
	} else if !hasLetterOrDigit(name) {
		add("name contains no letters or digits")
		r.Spam = true
	}
	if runeLen(name) > maxNameLen {
		add("name too long: %d chars", runeLen(name))
		r.Spam = true
	}
	if w, n := stuffedWord(name); n >= stuffingMinRepeat {
		add("word %q repeated %d times (keyword stuffing)", w, n)
		r.Spam = true
	}
	if ratio := nonAlnumRatio(name); ratio > nonAlnumMaxRatio {
		add("non-alphanumeric ratio %.2f exceeds %.2f", ratio, nonAlnumMaxRatio)
		r.Spam = true
	}
	if totalSize <= 0 {
		add("zero or negative total size")
		r.Spam = true
	} else if totalSize > maxTotalSize {
		add("total size %d exceeds 20 TiB", totalSize)
		r.Spam = true
	}
	if len(files) > maxFileCount {
		add("file count %d exceeds %d", len(files), maxFileCount)
		r.Spam = true
	}
	if len(files) >= randomMinFiles {
		n := 0
		for _, f := range files {
			if looksRandom(f.Path) {
				n++
			}
		}
		if float64(n)/float64(len(files)) > randomMaxRatio {
			add("%d/%d file names look like random strings", n, len(files))
			r.Spam = true
		}
	}

	return r
}

// hasLetterOrDigit reports whether s contains any letter (incl. CJK) or digit.
func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func runeLen(s string) int { return len([]rune(s)) }

// stuffedWord returns the most repeated word and its count.
func stuffedWord(s string) (string, int) {
	counts := map[string]int{}
	best, bestN := "", 0
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) < 2 {
			continue
		}
		counts[w]++
		if counts[w] > bestN {
			best, bestN = w, counts[w]
		}
	}
	return best, bestN
}

// nonAlnumRatio returns the fraction of runes that are not letters, digits
// or whitespace.
func nonAlnumRatio(s string) float64 {
	total, bad := 0, 0
	for _, r := range s {
		total++
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r) {
			bad++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(bad) / float64(total)
}

// looksRandom reports whether the file's base name looks like a random
// string: 20+ consecutive alphanumerics without any vowel.
func looksRandom(path string) bool {
	base := path
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	if len(base) < randomMinLen {
		return false
	}
	for i := 0; i < len(base); i++ {
		c := base[i]
		isAlnum := c < 128 && unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c))
		if !isAlnum {
			return false
		}
		switch c | 0x20 {
		case 'a', 'e', 'i', 'o', 'u':
			return false
		}
	}
	return true
}

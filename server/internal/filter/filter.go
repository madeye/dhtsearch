// Package filter detects adult, spam and undersized torrent metadata. Adult
// signals are persisted as classifications; spam and undersized records are
// dropped by the ingestion pipeline.
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
	Adult    bool     `json:"adult"`
	Spam     bool     `json:"spam"`
	TooSmall bool     `json:"too_small"`
	Reasons  []string `json:"reasons,omitempty"`
}

// Rejected reports whether any signal fired. It is retained for corpus
// evaluation; ingestion handles Adult as a classification rather than a drop.
func (r Result) Rejected() bool { return r.Adult || r.Spam || r.TooSmall }

// MinTotalSize is the smallest torrent worth indexing. Anything below it is
// dropped as junk (fake stubs, single images, link/readme-only torrents).
// Overridable at startup from MIN_TORRENT_SIZE.
var MinTotalSize int64 = 100 << 20 // 100 MiB

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
	javStudioRe   = regexp.MustCompile(javStudioPattern)

	// matcher for adultKeywords, applied to the name and every file path.
	kwAll matcher
	// matcher for adultNameKeywords, applied to the name only.
	kwName matcher
)

// matcher matches a keyword list: plain substring search for CJK and longer
// tokens, word-boundary regex for short ASCII tokens.
type matcher struct {
	substrings []string
	wordRe     *regexp.Regexp
}

func newMatcher(keywords []string) matcher {
	var m matcher
	var shorts []string
	for _, kw := range keywords {
		kw = strings.ToLower(kw)
		if isShortASCII(kw) {
			shorts = append(shorts, regexp.QuoteMeta(kw))
		} else {
			m.substrings = append(m.substrings, kw)
		}
	}
	if len(shorts) > 0 {
		m.wordRe = regexp.MustCompile(`\b(?:` + strings.Join(shorts, "|") + `)\b`)
	}
	return m
}

// find returns the first keyword found in the lowercased text, or "".
func (m matcher) find(text string) string {
	for _, kw := range m.substrings {
		if strings.Contains(text, kw) {
			return kw
		}
	}
	if m.wordRe != nil {
		return m.wordRe.FindString(text)
	}
	return ""
}

func init() {
	kwAll = newMatcher(adultKeywords)
	kwName = newMatcher(adultNameKeywords)
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

// Check inspects a torrent's name, file list and total size and returns all
// detected signals.
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
		for _, ex := range adultExceptions {
			text = strings.ReplaceAll(text, strings.ToLower(ex), "")
		}
		if kw := kwAll.find(text); kw != "" {
			add("adult keyword %q in %s", kw, what)
			adultHit = true
			return
		}
		if m := adultDomainRe.FindString(text); m != "" {
			add("adult site domain %q in %s", m, what)
			adultHit = true
		}
	}
	checkAdult("name", lowerName)
	// Adult-forum banners count only in the name: ad files carrying the same
	// banners get bundled into legitimate uploads.
	if kw := kwName.find(lowerName); kw != "" && !adultHit {
		add("adult site banner %q in name", kw)
		adultHit = true
	}
	// A known JAV studio code is a strong signal on its own.
	if m := javStudioRe.FindString(lowerName); m != "" && !adultHit {
		add("jav studio code %q in name", m)
		adultHit = true
	}
	for _, f := range files {
		checkAdult("file", strings.ToLower(f.Path))
	}
	// A generic release-code shape is a weak signal: only flag when another
	// adult signal hit.
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

	// --- Size floor ---
	// Distinct from spam: the torrent may be perfectly legitimate, just too
	// small to be worth indexing.
	if totalSize > 0 && totalSize < MinTotalSize {
		add("total size %d below minimum %d", totalSize, MinTotalSize)
		r.TooSmall = true
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

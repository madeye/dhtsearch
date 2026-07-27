package filter

// Corpus replay harness: measures the keyword filter against titles the LLM
// moderator has already labeled. Run manually with two line-delimited files:
//
//	FILTER_REMOVED=removed.txt FILTER_OK=ok.txt go test -run TestCorpusReplay -v ./internal/filter
//
// FILTER_REMOVED holds `<label> <hash> "<title>"` lines from the moderator
// journal; FILTER_OK holds bare titles the LLM approved. The test is skipped
// when the variables are unset, so CI never depends on the corpus files.

import (
	"bufio"
	"os"
	"regexp"
	"testing"
)

var removedLineRe = regexp.MustCompile(`^(adult|spam) [0-9a-f]{40} "(.*)"$`)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCorpusReplay(t *testing.T) {
	removedPath, okPath := os.Getenv("FILTER_REMOVED"), os.Getenv("FILTER_OK")
	if removedPath == "" || okPath == "" {
		t.Skip("set FILTER_REMOVED and FILTER_OK to run the corpus replay")
	}

	var caught, total int
	for _, line := range readLines(t, removedPath) {
		m := removedLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		total++
		if Check(m[2], nil, 1<<30).Rejected() {
			caught++
		}
	}
	t.Logf("removed corpus: caught %d/%d (%.1f%%)", caught, total, 100*float64(caught)/float64(total))

	var fp int
	for _, name := range readLines(t, okPath) {
		if r := Check(name, nil, 1<<30); r.Adult || r.Spam {
			fp++
			t.Logf("FP: %v  %q", r.Reasons, name)
		}
	}
	t.Logf("approved corpus: %d false positives / %d titles", fp, len(readLines(t, okPath)))
}

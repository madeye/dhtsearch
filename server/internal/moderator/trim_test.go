package moderator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCleanTitleAcceptsDeletionOnly(t *testing.T) {
	tests := []struct {
		name, raw, proposed, want string
	}{
		{
			name:     "leading site banner",
			raw:      "【高清剧集网发布 www.abc.com】三体 第二季 全集[国语中字]",
			proposed: "三体 第二季 全集[国语中字]",
			want:     "三体 第二季 全集[国语中字]",
		},
		{
			name:     "bracketed domain prefix",
			raw:      "[ www.UsaBit.com ] - The Jungle Book 1967 720p BRRip x264-PLAYNOW.mp4",
			proposed: "The Jungle Book 1967 720p BRRip x264-PLAYNOW.mp4",
			want:     "The Jungle Book 1967 720p BRRip x264-PLAYNOW.mp4",
		},
		{
			name:     "trailing uploader tag",
			raw:      "The Simpsons S32E18 1080p DSNP WEB-DL DDP5 1 H 264-playWEB[TGx]",
			proposed: "The Simpsons S32E18 1080p DSNP WEB-DL DDP5 1 H 264-playWEB",
			want:     "The Simpsons S32E18 1080p DSNP WEB-DL DDP5 1 H 264-playWEB",
		},
		{
			name:     "gap left by an excised span is squeezed",
			raw:      "Movie 2003 www.spam.net 1080p",
			proposed: "Movie 2003  1080p",
			want:     "Movie 2003 1080p",
		},
		{
			name:     "stray separator left behind is trimmed",
			raw:      "[ www.UsaBit.com ] - The Jungle Book",
			proposed: "- The Jungle Book",
			want:     "The Jungle Book",
		},
		// Rejections: anything that is not pure deletion, or cuts too deep.
		{
			name:     "unchanged title stores nothing",
			raw:      "Ubuntu 24.04 ISO",
			proposed: "Ubuntu 24.04 ISO",
			want:     "",
		},
		{
			name:     "reworded title is rejected",
			raw:      "Dune Part Two 2024 2160p",
			proposed: "Dune: Part 2 (2024) 4K",
			want:     "",
		},
		{
			name:     "translated title is rejected",
			raw:      "三体 第二季",
			proposed: "Three Body Season 2",
			want:     "",
		},
		{
			name:     "reordered title is rejected",
			raw:      "Alpha Beta Gamma",
			proposed: "Gamma Alpha",
			want:     "",
		},
		{
			name:     "hallucinated addition is rejected",
			raw:      "Movie 2003 1080p",
			proposed: "Movie 2003 1080p BluRay",
			want:     "",
		},
		{
			name:     "empty proposal is rejected",
			raw:      "Movie 2003 1080p",
			proposed: "   ",
			want:     "",
		},
		{
			name:     "over-trimming below the ratio floor is rejected",
			raw:      "Some Quite Long Movie Title 2003 1080p BluRay x264",
			proposed: "Some",
			want:     "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanTitle(tc.raw, tc.proposed); got != tc.want {
				t.Errorf("cleanTitle(%q, %q) = %q, want %q", tc.raw, tc.proposed, got, tc.want)
			}
		})
	}
}

// A title too long for the prompt is shown truncated, so the model never sees
// its tail. Applying the answer would silently delete the rest of the name.
func TestCleanTitleSkipsTruncatedNames(t *testing.T) {
	raw := strings.Repeat("A", promptNameCap+50)
	if got := cleanTitle(raw, strings.Repeat("A", promptNameCap)); got != "" {
		t.Errorf("cleanTitle on truncated name = %q, want empty", got)
	}
}

func TestIsSubsequence(t *testing.T) {
	tests := []struct {
		sub, of string
		want    bool
	}{
		{"abc", "abc", true},
		{"ac", "abc", true},
		{"", "abc", true},
		{"abc", "ab", false},
		{"ba", "abc", false},
		{"三体", "【广告】三体 第二季", true},
	}
	for _, tc := range tests {
		if got := isSubsequence([]rune(tc.sub), []rune(tc.of)); got != tc.want {
			t.Errorf("isSubsequence(%q, %q) = %v, want %v", tc.sub, tc.of, got, tc.want)
		}
	}
}

func TestSystemPromptOmitsTrimSectionWhenDisabled(t *testing.T) {
	on, off := systemPromptFor(true), systemPromptFor(false)
	if !strings.Contains(on, "CLEAN") || !strings.Contains(on, `"clean"`) {
		t.Error("trim-enabled prompt is missing the CLEAN section")
	}
	if strings.Contains(off, "CLEAN") || strings.Contains(off, `"clean"`) {
		t.Error("trim-disabled prompt still asks for cleaned titles")
	}
	for _, p := range []string{on, off} {
		if !strings.Contains(p, `"adult"`) || !strings.Contains(p, `"spam"`) {
			t.Error("prompt lost its moderation labels")
		}
	}
}

// trimAPI serves an OpenAI-compatible endpoint that labels everything "ok" and
// returns clean(title) as the trimmed title.
func trimAPI(t *testing.T, clean func(line string) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		type v struct {
			I     int    `json:"i"`
			Label string `json:"label"`
			Clean string `json:"clean"`
		}
		var items []promptItem
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &items); err != nil {
			t.Fatalf("listing is not JSON: %v (%q)", err, req.Messages[1].Content)
		}
		var out []v
		for _, it := range items {
			out = append(out, v{I: it.I, Label: "ok", Clean: clean(it.Title)})
		}
		content, _ := json.Marshal(map[string]any{"verdicts": out})
		json.NewEncoder(w).Encode(chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Content: string(content)}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSweepTrimsAdvertisingFromTitles(t *testing.T) {
	st := newStore(t)
	const adTitle = "【发布页 www.spam.net】Blue Bloods S14E03 1080p AMZN WEB-DL"
	seed(t, st, adTitle, "Ubuntu 24.04 ISO")

	srv := trimAPI(t, func(title string) string {
		return strings.TrimPrefix(title, "【发布页 www.spam.net】")
	})
	m := newMod(t, st, srv.URL, func(c *Config) { c.TrimTitles = true })

	s, err := m.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Trimmed != 1 {
		t.Errorf("Trimmed = %d, want 1", s.Trimmed)
	}

	items, _, err := st.Search(t.Context(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, it := range items {
		names[it.Name] = true
	}
	if !names["Blue Bloods S14E03 1080p AMZN WEB-DL"] {
		t.Errorf("search did not return the trimmed title, got %v", names)
	}
	if names[adTitle] {
		t.Error("search still returns the raw advertising title")
	}
	// The untouched title must survive verbatim.
	if !names["Ubuntu 24.04 ISO"] {
		t.Errorf("clean title was altered, got %v", names)
	}
}

// The raw title stays searchable, so trimming can never hide a result.
func TestTrimmedTorrentIsStillFoundByRawText(t *testing.T) {
	st := newStore(t)
	seed(t, st, "【发布页 www.spam.net】Blue Bloods S14E03 1080p")

	srv := trimAPI(t, func(title string) string {
		return strings.TrimPrefix(title, "【发布页 www.spam.net】")
	})
	m := newMod(t, st, srv.URL, func(c *Config) { c.TrimTitles = true })
	if _, err := m.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"spam.net", "Blue Bloods"} {
		items, total, err := st.Search(t.Context(), q, 1, 10)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(items) != 1 {
			t.Errorf("Search(%q) total = %d, want 1", q, total)
		}
	}
}

func TestTrimDisabledLeavesTitlesAlone(t *testing.T) {
	st := newStore(t)
	const adTitle = "【发布页 www.spam.net】Blue Bloods S14E03 1080p"
	seed(t, st, adTitle)

	srv := trimAPI(t, func(line string) string { return "Blue Bloods S14E03 1080p" })
	m := newMod(t, st, srv.URL, func(c *Config) { c.TrimTitles = false })
	s, err := m.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Trimmed != 0 {
		t.Errorf("Trimmed = %d, want 0", s.Trimmed)
	}
	items, _, err := st.Search(t.Context(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Name != adTitle {
		t.Errorf("Name = %q, want the raw title %q", items[0].Name, adTitle)
	}
}

func TestDryRunTrimsNothing(t *testing.T) {
	st := newStore(t)
	const adTitle = "【发布页 www.spam.net】Blue Bloods S14E03 1080p"
	seed(t, st, adTitle)

	srv := trimAPI(t, func(line string) string { return "Blue Bloods S14E03 1080p" })
	m := newMod(t, st, srv.URL, func(c *Config) { c.TrimTitles, c.DryRun = true, true })
	if _, err := m.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _, err := st.Search(t.Context(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Name != adTitle {
		t.Errorf("dry run modified the title: got %q", items[0].Name)
	}
}

// A model that rewrites rather than deletes must not reach the database.
func TestSweepRejectsRewrittenTitles(t *testing.T) {
	st := newStore(t)
	const raw = "Dune Part Two 2024 2160p"
	seed(t, st, raw)

	srv := trimAPI(t, func(line string) string { return "Dune: Part 2 (2024) 4K" })
	m := newMod(t, st, srv.URL, func(c *Config) { c.TrimTitles = true })
	s, err := m.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Trimmed != 0 {
		t.Errorf("Trimmed = %d, want 0", s.Trimmed)
	}
	items, _, err := st.Search(t.Context(), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Name != raw {
		t.Errorf("Name = %q, want %q", items[0].Name, raw)
	}
}

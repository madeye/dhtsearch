package feeds

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// dmhyRSS mirrors share.dmhy.org's format: base32 btih in the enclosure.
const dmhyRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>dmhy</title>
<item>
<title><![CDATA[ 【合集】【2026春季网络日剧】[MagicStar]  地狱占星师  / 地獄に堕ちるわよ [WEBDL] [1080p]]]></title>
<link>http://share.dmhy.org/topics/view/717995_2026_MagicStar_WEBDL_1080p_Netflix.html</link>
<enclosure url="magnet:?xt=urn:btih:2XUU4VPG3OIUEPGLK7ZYILRYUHI2SGD2&amp;dn=&amp;tr=http%3A%2F%2F104.143.10.61%3A8000%2Fannounce" length="1" type="application/x-bittorrent"></enclosure>
</item>
<item><title>no hash here</title><link>http://share.dmhy.org/topics/view/1.html</link></item>
</channel></rss>`

// nyaaRSS mirrors nyaa.si's format: hex hash in <nyaa:infoHash>.
const nyaaRSS = `<?xml version="1.0" encoding="utf-8"?>
<rss xmlns:atom="http://www.w3.org/2005/Atom" xmlns:nyaa="https://nyaa.si/xmlns/nyaa" version="2.0">
<channel><title>Nyaa</title>
<item>
<title>[NanakoRaws] Kamen Rider My-th - 05 (NBN TV 1080p HEVC AAC)</title>
<link>https://nyaa.si/download/2169933.torrent</link>
<nyaa:infoHash>68A0EF392F89FC45926BEA2C7D68C935ABEF9415</nyaa:infoHash>
</item>
</channel></rss>`

const jigokuHash = "d5e94e55e6db91423ccb57f3842e38a1d1a9187a"

func TestParse(t *testing.T) {
	items, err := Parse([]byte(dmhyRSS))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("dmhy items=%+v, want 1 (hashless item skipped)", items)
	}
	if items[0].InfoHash != jigokuHash {
		t.Fatalf("base32 hash decoded to %s", items[0].InfoHash)
	}
	if items[0].Title != "【合集】【2026春季网络日剧】[MagicStar] 地狱占星师 / 地獄に堕ちるわよ [WEBDL] [1080p]" {
		t.Fatalf("title not whitespace-normalized: %q", items[0].Title)
	}

	items, err = Parse([]byte(nyaaRSS))
	if err != nil || len(items) != 1 || items[0].InfoHash != "68a0ef392f89fc45926bea2c7d68c935abef9415" {
		t.Fatalf("nyaa: items=%+v err=%v", items, err)
	}

	if _, err := Parse([]byte("<html>blocked</html")); err == nil {
		t.Fatal("garbage must be an error")
	}
}

func TestNormalizeHash(t *testing.T) {
	for in, want := range map[string]string{
		"2XUU4VPG3OIUEPGLK7ZYILRYUHI2SGD2":         jigokuHash,
		"2xuu4vpg3oiuepglk7zyilryuhi2sgd2":         jigokuHash,
		strings.ToUpper(jigokuHash):                jigokuHash,
		"zz" + jigokuHash[2:]:                      "",
		"short":                                    "",
		"magnet:?xt=urn:btih:" + jigokuHash + "&x": "",
	} {
		if got := normalizeHash(in); got != want {
			t.Errorf("normalizeHash(%q)=%q want %q", in, got, want)
		}
	}
}

type fakeStore struct {
	mu      sync.Mutex
	indexed map[string]string // hash -> alias
	blocked map[string]bool
}

func (f *fakeStore) SetAlias(h, a string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.indexed[h]; !ok {
		return false, nil
	}
	f.indexed[h] = a
	return true, nil
}

func (f *fakeStore) IsBlocked(h string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked[h], nil
}

func newTestService(t *testing.T, st Store, feeds ...string) (*Service, *time.Time) {
	t.Helper()
	s := New(Config{Feeds: feeds, Store: st, Buffer: 8, Logger: log.New(io.Discard, "", 0)})
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}

func drain(c <-chan string) (got []string) {
	for {
		select {
		case h := <-c:
			got = append(got, h)
		default:
			return got
		}
	}
}

func serve(t *testing.T, bodies map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPollQueuesUnknownAndAliasesIndexed(t *testing.T) {
	srv := serve(t, map[string]string{"/dmhy": dmhyRSS, "/nyaa": nyaaRSS})
	st := &fakeStore{
		indexed: map[string]string{"68a0ef392f89fc45926bea2c7d68c935abef9415": ""},
		blocked: map[string]bool{},
	}
	s, _ := newTestService(t, st, srv.URL+"/dmhy", srv.URL+"/nyaa", srv.URL+"/missing")
	s.poll(t.Context())

	if got := drain(s.Out()); len(got) != 1 || got[0] != jigokuHash {
		t.Fatalf("queued=%v, want only the unindexed hash", got)
	}
	if a := s.Alias(jigokuHash); !strings.Contains(a, "地狱占星师") {
		t.Fatalf("alias for queued hash = %q", a)
	}
	if a := st.indexed["68a0ef392f89fc45926bea2c7d68c935abef9415"]; !strings.HasPrefix(a, "[NanakoRaws]") {
		t.Fatalf("indexed torrent not aliased: %q", a)
	}
	ss := s.Stats()
	if ss.Polls != 1 || ss.Items != 2 || ss.Queued != 1 || ss.Aliased != 1 || ss.Errors != 1 {
		t.Fatalf("stats=%+v", ss)
	}
}

func TestRetryBackoffAndCap(t *testing.T) {
	srv := serve(t, map[string]string{"/dmhy": dmhyRSS})
	st := &fakeStore{indexed: map[string]string{}, blocked: map[string]bool{}}
	s, now := newTestService(t, st, srv.URL+"/dmhy")

	queued := 0
	for i := 0; i < 10; i++ {
		s.poll(t.Context())
		queued += len(drain(s.Out()))
		*now = now.Add(time.Hour)
	}
	// 10 hourly polls: tries at h0 and h6 only (retryAfter=6h).
	if queued != 2 {
		t.Fatalf("queued %d times in 10h, want 2", queued)
	}
	for i := 0; i < 30; i++ {
		s.poll(t.Context())
		queued += len(drain(s.Out()))
		*now = now.Add(time.Hour)
	}
	if queued != maxAttempts {
		t.Fatalf("queued %d times total, want cap %d", queued, maxAttempts)
	}

	// Once it is indexed (fetched, or synced in), the next poll aliases it.
	st.indexed[jigokuHash] = ""
	s.poll(t.Context())
	if st.indexed[jigokuHash] == "" {
		t.Fatal("alias not applied after the torrent got indexed")
	}
}

func TestBlockedIsNeverQueued(t *testing.T) {
	srv := serve(t, map[string]string{"/dmhy": dmhyRSS})
	st := &fakeStore{indexed: map[string]string{}, blocked: map[string]bool{jigokuHash: true}}
	s, _ := newTestService(t, st, srv.URL+"/dmhy")
	s.poll(t.Context())
	if got := drain(s.Out()); len(got) != 0 {
		t.Fatalf("blocked hash queued: %v", got)
	}
}

func TestFullBufferDoesNotSpendAttempt(t *testing.T) {
	srv := serve(t, map[string]string{"/dmhy": dmhyRSS})
	st := &fakeStore{indexed: map[string]string{}, blocked: map[string]bool{}}
	s := New(Config{Feeds: []string{srv.URL + "/dmhy"}, Store: st, Buffer: 1,
		Logger: log.New(io.Discard, "", 0)})
	s.out <- "filler"
	s.poll(t.Context())
	<-s.out // filler
	s.poll(t.Context())
	if got := drain(s.Out()); len(got) != 1 || got[0] != jigokuHash {
		t.Fatalf("hash not offered after the buffer freed up: %v", got)
	}
}

func TestKeywordSearch(t *testing.T) {
	defer func(d time.Duration) { keywordDelay = d }(keywordDelay)
	keywordDelay = time.Millisecond
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("keyword"))
		mu.Unlock()
		io.WriteString(w, dmhyRSS)
	}))
	defer srv.Close()
	st := &fakeStore{indexed: map[string]string{}, blocked: map[string]bool{}}
	s := New(Config{
		KeywordURL: srv.URL + "/rss.xml?keyword=%s",
		Keywords:   func() []string { return []string{"地狱占星师", " ", "思想验证区域 第二季"} },
		Store:      st,
		Logger:     log.New(io.Discard, "", 0),
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s.poll(ctx)
	if len(queries) != 2 || queries[0] != "地狱占星师" || queries[1] != "思想验证区域 第二季" {
		t.Fatalf("keyword queries=%q", queries)
	}
	if got := drain(s.Out()); len(got) != 1 {
		t.Fatalf("same hash from two searches must queue once: %v", got)
	}
}

func TestPrioritize(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hi := make(chan string, 4)
	lo := make(chan string, 4)
	lo <- "lo1"
	lo <- "lo2"
	hi <- "hi1"
	hi <- "hi2"
	out := Prioritize(ctx, hi, lo)
	// The merger may have taken one value before hi was filled; after that,
	// hi must drain before lo resumes.
	var got []string
	for i := 0; i < 4; i++ {
		got = append(got, <-out)
	}
	pos := map[string]int{}
	for i, v := range got {
		pos[v] = i
	}
	if pos["hi2"] > pos["lo2"] {
		t.Fatalf("order %v: hi not prioritized", got)
	}

	close(hi)
	lo <- "lo3"
	if v := <-out; v != "lo3" {
		t.Fatalf("after hi closed got %q", v)
	}
	cancel()
	for range out {
	}
}

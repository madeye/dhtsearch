package scraper

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"testing"
	"time"
)

// hashN builds a valid distinct hex infohash from n.
func hashN(n byte) string {
	var ih [20]byte
	ih[0] = n
	ih[19] = 1 // never the all-zero hash
	return hex.EncodeToString(ih[:])
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// fakeSeeders installs a scrape stub that reports seeders[hash] for known
// hashes and 0 otherwise.
func fakeSeeders(s *Scraper, seeders map[string]int32) {
	s.scrapeTracker = func(_ context.Context, _ *trackerConn, ihs [][20]byte) ([]int32, error) {
		out := make([]int32, len(ihs))
		for i, ih := range ihs {
			out[i] = seeders[hex.EncodeToString(ih[:])]
		}
		return out, nil
	}
}

func recvOrFail(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case h := <-ch:
		return h
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a prioritized hash")
		return ""
	}
}

func TestNewRejectsUnusableTrackers(t *testing.T) {
	_, err := New(Config{
		Trackers: []string{"https://tracker.example/announce", "not a url at all", "udp://"},
		Logger:   quietLogger(),
	})
	if err == nil {
		t.Fatal("want error when no tracker is scrapeable")
	}
}

func TestTrackerHost(t *testing.T) {
	host, err := trackerHost("udp://tracker.opentrackr.org:1337/announce")
	if err != nil {
		t.Fatal(err)
	}
	if host != "tracker.opentrackr.org:1337" {
		t.Fatalf("host = %q", host)
	}
	if _, err := trackerHost("http://tracker.example/announce"); err == nil {
		t.Fatal("want error for non-udp scheme")
	}
}

func TestPrioritizesBySeeders(t *testing.T) {
	s, err := New(Config{
		Trackers: []string{"udp://tracker.invalid:1337/announce"},
		BatchMax: 4,
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	low, mid, high, zero := hashN(1), hashN(2), hashN(3), hashN(4)
	fakeSeeders(s, map[string]int32{low: 2, mid: 40, high: 900})

	in := make(chan string, 4)
	for _, h := range []string{zero, low, high, mid} { // one full batch
		in <- h
	}
	s.Run(context.Background(), in)
	defer s.Close()

	want := []string{high, mid, low, zero}
	for i, w := range want {
		if got := recvOrFail(t, s.Out()); got != w {
			t.Fatalf("position %d: got %s want %s", i, got, w)
		}
	}
	st := s.Stats()
	if st.Scraped != 4 || st.Seeded != 3 {
		t.Fatalf("stats = %+v, want Scraped=4 Seeded=3", st)
	}
}

func TestQueueEvictsLowestPriority(t *testing.T) {
	s, err := New(Config{
		Trackers: []string{"udp://tracker.invalid:1337/announce"},
		BatchMax: 4,
		QueueCap: 2,
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h1, h2, h3, h4 := hashN(1), hashN(2), hashN(3), hashN(4)
	fakeSeeders(s, map[string]int32{h1: 9, h2: 7, h3: 5, h4: 3})

	in := make(chan string, 4)
	for _, h := range []string{h4, h2, h1, h3} { // one full batch
		in <- h
	}
	close(in) // out closes once everything surviving has been delivered
	s.Run(context.Background(), in)
	defer s.Close()

	// The batch lands in the queue atomically, so the cap keeps the top two.
	var got []string
	for h := range s.Out() {
		got = append(got, h)
	}
	want := []string{h1, h2}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
	if ev := s.Stats().Evicted; ev != 2 {
		t.Fatalf("Evicted = %d, want 2", ev)
	}
}

func TestPartialBatchFlushesOnTimer(t *testing.T) {
	s, err := New(Config{
		Trackers:  []string{"udp://tracker.invalid:1337/announce"},
		BatchMax:  70,
		BatchWait: 50 * time.Millisecond,
		Logger:    quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := hashN(7)
	fakeSeeders(s, map[string]int32{h: 1})

	in := make(chan string, 1)
	in <- h
	s.Run(context.Background(), in)
	defer s.Close()

	if got := recvOrFail(t, s.Out()); got != h {
		t.Fatalf("got %s want %s", got, h)
	}
}

func TestAllTrackersFailingStillDelivers(t *testing.T) {
	s, err := New(Config{
		Trackers: []string{"udp://tracker.invalid:1337/announce"},
		BatchMax: 2,
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.scrapeTracker = func(context.Context, *trackerConn, [][20]byte) ([]int32, error) {
		return nil, fmt.Errorf("tracker down")
	}
	h1, h2 := hashN(1), hashN(2)
	in := make(chan string, 2)
	in <- h1
	in <- h2
	s.Run(context.Background(), in)
	defer s.Close()

	got := map[string]bool{recvOrFail(t, s.Out()): true, recvOrFail(t, s.Out()): true}
	if !got[h1] || !got[h2] {
		t.Fatalf("delivered %v, want both %s and %s", got, h1, h2)
	}
	if errs := s.Stats().ScrapeErrs; errs == 0 {
		t.Fatal("want ScrapeErrs > 0")
	}
}

func TestMalformedHashesAreSkipped(t *testing.T) {
	s, err := New(Config{
		Trackers: []string{"udp://tracker.invalid:1337/announce"},
		BatchMax: 3,
		Logger:   quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	good := hashN(5)
	fakeSeeders(s, map[string]int32{good: 3})

	in := make(chan string, 3)
	in <- "zzzz"     // not hex
	in <- "deadbeef" // wrong length
	in <- good
	s.Run(context.Background(), in)
	defer s.Close()

	if got := recvOrFail(t, s.Out()); got != good {
		t.Fatalf("got %s want %s", got, good)
	}
	if st := s.Stats(); st.Scraped != 1 {
		t.Fatalf("Scraped = %d, want 1 (malformed skipped)", st.Scraped)
	}
}

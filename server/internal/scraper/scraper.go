// Package scraper ranks discovered infohashes by swarm size before metadata
// fetching. Discovery yields far more infohashes than the fetch pool can
// consume, so which ones get a fetch slot decides what the index grows into.
// A BEP 15 UDP scrape answers "how many seeders?" for up to ~70 infohashes in
// a single packet — three orders of magnitude cheaper than a metadata fetch —
// so every discovered hash is scraped against a few large open trackers and
// queued by seeder count. Popular swarms (which skew toward current video
// content) are fetched first; they are also the fetches most likely to
// succeed, since peers are plentiful.
//
// Zero-seeder hashes are not discarded: they queue at priority 0 and are
// fetched whenever nothing better is waiting, so DHT-only content still
// trickles in. When the queue overflows, the lowest-priority entries are the
// ones evicted — the same hashes that were previously dropped at random.
package scraper

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent/tracker/udp"
)

const (
	defaultQueueCap  = 1 << 14
	defaultBatchMax  = 70 // BEP 15 allows ~74 per packet; stay under
	defaultBatchWait = 2 * time.Second
	defaultTimeout   = 10 * time.Second

	// scrapeWorkers bounds concurrent in-flight batches. Each worker mostly
	// waits on UDP round trips, so they are cheap; the bound exists so dead
	// trackers (every batch riding out the full timeout) shed load by
	// dropping batches instead of queueing them without limit.
	scrapeWorkers  = 8
	batchQueueSize = 16
)

// Config controls the scraper.
type Config struct {
	// Trackers are udp:// announce URLs to scrape. Non-UDP entries are
	// skipped (HTTP scrape exists but no large open tracker needs it here).
	Trackers []string
	// QueueCap bounds the priority queue; overflow evicts the lowest
	// seeder counts first.
	QueueCap int
	// BatchMax is the most infohashes packed into one scrape request.
	BatchMax int
	// BatchWait flushes a partial batch that has waited this long.
	BatchWait time.Duration
	// Timeout is the budget for scraping one batch against one tracker.
	Timeout time.Duration
	// Logger, nil for log.Default().
	Logger *log.Logger
}

// Stats is a snapshot of scraper activity for the stats endpoint.
type Stats struct {
	QueueLen   int   // hashes waiting for a fetch slot
	Scraped    int64 // hashes scraped so far
	Seeded     int64 // of those, hashes with at least one seeder
	ScrapeErrs int64 // failed tracker requests (per tracker, per batch)
	Dropped    int64 // hashes dropped because all scrape workers were busy
	Evicted    int64 // hashes evicted from a full queue
}

type item struct {
	hash    string
	seeders int32
}

// trackerConn owns the UDP client for one tracker, recreating it after a
// failure (the conn client closes itself on read errors).
type trackerConn struct {
	host string // "host:port"
	mu   sync.Mutex
	cc   *udp.ConnClient
}

// Scraper consumes discovered infohashes, scrapes them and re-emits them in
// descending seeder order.
type Scraper struct {
	cfg      Config
	trackers []*trackerConn
	logger   *log.Logger

	out      chan string
	wake     chan struct{}
	feedDone chan struct{} // closed once nothing can enqueue anymore

	mu      sync.Mutex
	pending []item // sorted by seeders descending; FIFO among equals

	cancel context.CancelFunc
	wg     sync.WaitGroup

	scraped    atomic.Int64
	seeded     atomic.Int64
	scrapeErrs atomic.Int64
	dropped    atomic.Int64
	evicted    atomic.Int64

	// scrapeTracker is swapped in tests to avoid network I/O.
	scrapeTracker func(ctx context.Context, tc *trackerConn, ihs [][20]byte) ([]int32, error)
}

// New validates the tracker list and builds a scraper. At least one valid
// udp:// tracker is required.
func New(cfg Config) (*Scraper, error) {
	if cfg.QueueCap <= 0 {
		cfg.QueueCap = defaultQueueCap
	}
	if cfg.BatchMax <= 0 || cfg.BatchMax > defaultBatchMax {
		cfg.BatchMax = defaultBatchMax
	}
	if cfg.BatchWait <= 0 {
		cfg.BatchWait = defaultBatchWait
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	s := &Scraper{
		cfg:           cfg,
		logger:        logger,
		out:           make(chan string),
		wake:          make(chan struct{}, 1),
		feedDone:      make(chan struct{}),
		scrapeTracker: realScrape,
	}
	for _, raw := range cfg.Trackers {
		host, err := trackerHost(raw)
		if err != nil {
			logger.Printf("scraper: skipping tracker %q: %v", raw, err)
			continue
		}
		s.trackers = append(s.trackers, &trackerConn{host: host})
	}
	if len(s.trackers) == 0 {
		return nil, fmt.Errorf("no scrapeable udp trackers in %v", cfg.Trackers)
	}
	return s, nil
}

// trackerHost extracts "host:port" from a udp:// announce URL.
func trackerHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "udp" {
		return "", fmt.Errorf("scheme %q is not scrapeable over BEP 15", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host")
	}
	return u.Host, nil
}

// Run starts the pipeline consuming hex infohashes from in. The reordered
// hashes are delivered on Out, which closes when in closes or the scraper is
// closed.
func (s *Scraper) Run(ctx context.Context, in <-chan string) {
	ctx, s.cancel = context.WithCancel(ctx)
	batches := make(chan []string, batchQueueSize)
	var feedWG sync.WaitGroup
	feedWG.Add(1)
	go s.collect(ctx, in, batches, &feedWG)
	for i := 0; i < scrapeWorkers; i++ {
		feedWG.Add(1)
		go s.scrapeWorker(ctx, batches, &feedWG)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		feedWG.Wait()
		close(s.feedDone)
	}()
	s.wg.Add(1)
	go s.pushLoop(ctx)
	s.logger.Printf("scraper: ranking infohashes via %d trackers", len(s.trackers))
}

// Out returns the channel of prioritized hex infohashes.
func (s *Scraper) Out() <-chan string { return s.out }

// Stats returns a snapshot of the counters.
func (s *Scraper) Stats() Stats {
	s.mu.Lock()
	qlen := len(s.pending)
	s.mu.Unlock()
	return Stats{
		QueueLen:   qlen,
		Scraped:    s.scraped.Load(),
		Seeded:     s.seeded.Load(),
		ScrapeErrs: s.scrapeErrs.Load(),
		Dropped:    s.dropped.Load(),
		Evicted:    s.evicted.Load(),
	}
}

// Close stops the pipeline and closes tracker connections.
func (s *Scraper) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	for _, tc := range s.trackers {
		tc.mu.Lock()
		if tc.cc != nil {
			tc.cc.Close()
			tc.cc = nil
		}
		tc.mu.Unlock()
	}
}

// collect batches incoming hashes for scraping. It always keeps draining in:
// if the scrape workers are saturated, a batch is dropped rather than letting
// backpressure reach the crawler's non-blocking push.
func (s *Scraper) collect(ctx context.Context, in <-chan string, batches chan<- []string, wg *sync.WaitGroup) {
	defer wg.Done()
	defer close(batches)
	var batch []string
	timer := time.NewTimer(s.cfg.BatchWait)
	if !timer.Stop() {
		<-timer.C
	}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		select {
		case batches <- batch:
		default:
			s.dropped.Add(int64(len(batch)))
		}
		batch = nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case h, ok := <-in:
			if !ok {
				flush()
				return
			}
			if len(batch) == 0 {
				timer.Reset(s.cfg.BatchWait)
			}
			batch = append(batch, h)
			if len(batch) >= s.cfg.BatchMax {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				flush()
			}
		case <-timer.C:
			flush()
		}
	}
}

func (s *Scraper) scrapeWorker(ctx context.Context, batches <-chan []string, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-batches:
			if !ok {
				return
			}
			s.scrapeBatch(ctx, batch)
		}
	}
}

// scrapeBatch queries every tracker in parallel and enqueues each hash with
// the highest seeder count any tracker reported. A hash unknown everywhere
// (or a batch where every tracker errored) enqueues at zero — deprioritized,
// not lost.
func (s *Scraper) scrapeBatch(ctx context.Context, hexes []string) {
	ihs := make([][20]byte, 0, len(hexes))
	kept := make([]string, 0, len(hexes))
	for _, h := range hexes {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 20 {
			continue
		}
		var ih [20]byte
		copy(ih[:], b)
		ihs = append(ihs, ih)
		kept = append(kept, h)
	}
	if len(ihs) == 0 {
		return
	}
	best := make([]int32, len(ihs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, tc := range s.trackers {
		wg.Add(1)
		go func(tc *trackerConn) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
			defer cancel()
			res, err := s.scrapeTracker(sctx, tc, ihs)
			if err != nil {
				s.scrapeErrs.Add(1)
				return
			}
			mu.Lock()
			for i, n := range res {
				if i < len(best) && n > best[i] {
					best[i] = n
				}
			}
			mu.Unlock()
		}(tc)
	}
	wg.Wait()

	items := make([]item, len(kept))
	var seededN int64
	for i, h := range kept {
		if best[i] > 0 {
			seededN++
		}
		items[i] = item{hash: h, seeders: best[i]}
	}
	s.scraped.Add(int64(len(kept)))
	s.seeded.Add(seededN)
	s.enqueue(items)
}

// enqueue merges a scraped batch into the priority queue. Sorting the whole
// slice per batch is deliberate: batches arrive every second or two and the
// queue is a few thousand entries, so a stable sort is both fast enough and
// gives FIFO order among equal seeder counts for free.
func (s *Scraper) enqueue(items []item) {
	s.mu.Lock()
	s.pending = append(s.pending, items...)
	sort.SliceStable(s.pending, func(i, j int) bool {
		return s.pending[i].seeders > s.pending[j].seeders
	})
	if n := len(s.pending); n > s.cfg.QueueCap {
		s.evicted.Add(int64(n - s.cfg.QueueCap))
		s.pending = append([]item(nil), s.pending[:s.cfg.QueueCap]...)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// pushLoop feeds the fetcher the current best hash, blocking on the
// unbuffered out channel so priority is applied as late as possible. It
// closes out once the queue is empty and nothing upstream can refill it,
// mirroring how the crawler's channel closes on shutdown.
func (s *Scraper) pushLoop(ctx context.Context) {
	defer s.wg.Done()
	defer close(s.out)
	for {
		s.mu.Lock()
		var next string
		ok := len(s.pending) > 0
		if ok {
			next = s.pending[0].hash
			s.pending = s.pending[1:]
		}
		s.mu.Unlock()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			case <-s.feedDone:
			}
			// Every enqueue happens before feedDone closes, so an empty
			// queue seen after this point is final.
			s.mu.Lock()
			empty := len(s.pending) == 0
			s.mu.Unlock()
			if empty {
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case s.out <- next:
		}
	}
}

// realScrape performs one BEP 15 scrape. BEP 15 defines the response as one
// result per requested infohash, in request order, so alignment is by index.
func realScrape(ctx context.Context, tc *trackerConn, ihs [][20]byte) ([]int32, error) {
	cc, err := tc.conn()
	if err != nil {
		return nil, err
	}
	resp, err := cc.Client.Scrape(ctx, ihs)
	if err != nil {
		tc.reset(cc)
		return nil, err
	}
	if len(resp) > len(ihs) {
		resp = resp[:len(ihs)]
	}
	out := make([]int32, len(resp))
	for i, r := range resp {
		out[i] = r.Seeders
	}
	return out, nil
}

func (tc *trackerConn) conn() (*udp.ConnClient, error) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.cc != nil {
		return tc.cc, nil
	}
	cc, err := udp.NewConnClient(udp.NewConnClientOpts{Network: "udp", Host: tc.host})
	if err != nil {
		return nil, err
	}
	tc.cc = cc
	return cc, nil
}

// reset drops the connection that just failed so the next scrape redials,
// unless another goroutine already replaced it.
func (tc *trackerConn) reset(old *udp.ConnClient) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.cc == old {
		old.Close()
		tc.cc = nil
	}
}

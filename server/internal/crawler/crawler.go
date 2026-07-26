// Package crawler runs a DHT node that passively observes get_peers,
// announce_peer and sample_infohashes traffic (BEP 5 / BEP 51) to discover
// infohashes, and actively queries known nodes to expand coverage.
package crawler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/int160"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent/metainfo"
)

// bootstrapNodes are well-known public DHT routers.
var bootstrapNodes = []string{
	"router.bittorrent.com:6881",
	"router.utorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"dht.aelitis.com:6881",
	"dht.libtorrent.org:25401",
}

const (
	seenCapacity  = 1 << 18 // ~260k infohashes remembered for dedup
	knownCapacity = 256     // recent infohashes used for active queries
	channelSize   = 4096

	// Sampling. Discovery is dominated by how many distinct nodes we can ask
	// for samples, so the samplers run concurrently over a queue of nodes
	// rather than probing one node on a timer.
	defaultSamplers = 32
	sampleTimeout   = 10 * time.Second
	nodeQueueSize   = 8192
	nodeTrackMax    = 1 << 16 // addresses remembered for resample scheduling

	// BEP 51 lets a node advertise how often its sample set is worth
	// re-reading. Clamp it: some nodes report absurd values, and a node we
	// never revisit is a node whose new torrents we never see.
	defaultResample = 15 * time.Minute
	minResample     = 1 * time.Minute
	maxResample     = 2 * time.Hour

	// Refilling the queue from the routing table, and widening it with
	// random-target find_node so we keep meeting nodes the table does not
	// already hold.
	refillInterval = 5 * time.Second
	walkInterval   = 2 * time.Second
)

// Config controls the crawler.
type Config struct {
	// Port is the UDP listen port; 0 picks a random one.
	Port int
	// Enabled=false disables the crawler entirely (offline environments).
	Enabled bool
	// Samplers is the number of concurrent BEP 51 sampling workers.
	// 0 selects defaultSamplers.
	Samplers int
	// Logger, nil for log.Default().
	Logger *log.Logger
}

// Crawler is a running (or disabled) DHT crawler.
type Crawler struct {
	enabled  bool
	samplers int
	server   *dht.Server
	out      chan string
	cancel   context.CancelFunc
	logger   *log.Logger

	mu    sync.Mutex
	seen  map[[20]byte]struct{}
	ring  [][20]byte // FIFO eviction order for seen
	known [][20]byte // recent infohashes for active probing

	// Sampling state. nodeQ carries nodes waiting to be sampled; nodeNext
	// records the earliest time each address is worth sampling again, which
	// keeps the samplers off nodes they just read.
	nodeQ    chan krpc.NodeAddr
	nodeMu   sync.Mutex
	nodeNext map[string]time.Time

	// Counters, read via Stats.
	statMu    sync.Mutex
	sampled   int64 // sample_infohashes queries answered
	sampleErr int64 // queries that failed or returned nothing
	harvested int64 // infohashes returned by samples (before dedup)
}

// Stats reports sampler activity. Nodes is the routing table size, which is
// the pool the samplers draw from — if it collapses, discovery stalls.
type Stats struct {
	Nodes     int
	Queued    int
	Sampled   int64
	SampleErr int64
	Harvested int64
}

// Start launches the crawler. With Enabled=false it returns a Crawler whose
// Infohashes channel is nil; all other methods remain safe to call.
func Start(cfg Config) (*Crawler, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	samplers := cfg.Samplers
	if samplers <= 0 {
		samplers = defaultSamplers
	}
	c := &Crawler{
		enabled:  cfg.Enabled,
		samplers: samplers,
		logger:   logger,
		seen:     make(map[[20]byte]struct{}, seenCapacity),
		nodeQ:    make(chan krpc.NodeAddr, nodeQueueSize),
		nodeNext: make(map[string]time.Time),
	}
	if !cfg.Enabled {
		return c, nil
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.Port})
	if err != nil {
		return nil, err
	}
	sc := dht.NewDefaultServerConfig()
	sc.Conn = conn
	sc.StartingNodes = func() ([]dht.Addr, error) {
		return dht.ResolveHostPorts(bootstrapNodes)
	}
	sc.OnQuery = c.onQuery
	sc.OnAnnouncePeer = func(infoHash metainfo.Hash, _ net.IP, _ int, _ bool) {
		c.push([20]byte(infoHash))
	}
	s, err := dht.NewServer(sc)
	if err != nil {
		conn.Close()
		return nil, err
	}
	c.server = s
	c.out = make(chan string, channelSize)

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	// The library does not run this itself: NewServer's contract is that the
	// caller starts it once the server is set up. Without it the routing
	// table is whatever the initial bootstrap returned and slowly decays, so
	// the samplers run out of nodes to ask. It bootstraps on its own, so an
	// explicit Bootstrap alongside it only races and logs "already
	// bootstrapping".
	go c.server.TableMaintainer()
	go c.refillLoop(ctx)
	go c.walkLoop(ctx)
	for i := 0; i < c.samplers; i++ {
		go c.sampleLoop(ctx)
	}
	logger.Printf("crawler: DHT node listening on %s (%d samplers)",
		conn.LocalAddr(), c.samplers)
	return c, nil
}

// Infohashes returns the channel of newly seen infohashes (hex encoded).
// It is nil when the crawler is disabled.
func (c *Crawler) Infohashes() <-chan string { return c.out }

// Enabled reports whether the crawler is running.
func (c *Crawler) Enabled() bool { return c.enabled }

// SeenCount returns how many unique infohashes are currently remembered.
func (c *Crawler) SeenCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

// Stats returns sampler counters. Safe on a disabled crawler.
func (c *Crawler) Stats() Stats {
	c.statMu.Lock()
	st := Stats{
		Sampled:   c.sampled,
		SampleErr: c.sampleErr,
		Harvested: c.harvested,
	}
	c.statMu.Unlock()
	if c.server != nil {
		st.Nodes = c.server.NumNodes()
	}
	st.Queued = len(c.nodeQ)
	return st
}

// Close shuts the crawler down.
func (c *Crawler) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.server != nil {
		c.server.Close()
	}
	if c.out != nil {
		close(c.out)
	}
}

// onQuery observes inbound queries. It always returns true so the default
// handlers reply (nodes/tokens/samples) — the node stays a good DHT citizen.
func (c *Crawler) onQuery(query *krpc.Msg, _ net.Addr) bool {
	if query.A == nil {
		return true
	}
	switch query.Q {
	case "get_peers":
		c.push(query.A.InfoHash)
	}
	return true
}

// push records an infohash if new, with FIFO eviction over the capacity.
func (c *Crawler) push(ih [20]byte) {
	c.mu.Lock()
	if _, ok := c.seen[ih]; ok {
		c.mu.Unlock()
		return
	}
	if len(c.ring) >= seenCapacity {
		delete(c.seen, c.ring[0])
		c.ring = c.ring[1:]
	}
	c.seen[ih] = struct{}{}
	c.ring = append(c.ring, ih)
	if len(c.known) >= knownCapacity {
		c.known = c.known[1:]
	}
	c.known = append(c.known, ih)
	out := c.out
	c.mu.Unlock()

	if out != nil {
		select {
		case out <- hex.EncodeToString(ih[:]):
		default: // downstream busy; drop rather than block the DHT handler
		}
	}
}

// refillLoop keeps the sampler queue fed from the routing table. Nodes that
// were sampled recently are skipped by enqueue, so this re-offers the whole
// table cheaply and only the due ones get through.
func (c *Crawler) refillLoop(ctx context.Context) {
	t := time.NewTicker(refillInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, n := range c.server.Nodes() {
			c.enqueueNode(n.Addr)
		}
	}
}

// walkLoop widens coverage beyond the routing table. find_node against a
// random target returns the nodes closest to it, which is how we reach parts
// of the ID space our own table does not cover — and each new node is a new
// sample set. Without this the samplers keep re-reading the same neighbours.
func (c *Crawler) walkLoop(ctx context.Context) {
	t := time.NewTicker(walkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		nodes := c.server.Nodes()
		if len(nodes) == 0 {
			continue
		}
		node := nodes[randIntn(len(nodes))]
		res := c.server.FindNode(
			dht.NewAddr(node.Addr.UDP()), int160.FromByteArray(randomID()),
			dht.QueryRateLimiting{},
		)
		c.absorbNodes(res)

		// Piggyback a get_peers for something recently seen: it keeps this
		// node in other peers' routing tables, which is what makes inbound
		// get_peers traffic (the passive half of discovery) arrive at all.
		if ih, ok := c.randomKnown(); ok {
			c.server.GetPeers(ctx, dht.NewAddr(node.Addr.UDP()),
				int160.FromByteArray(ih), false, dht.QueryRateLimiting{})
		}
	}
}

// sampleLoop pulls nodes off the queue and asks each for a sample of the
// infohashes it knows (BEP 51). This is the active half of discovery and the
// one that scales: every distinct node answers with up to a few dozen
// infohashes we would otherwise have to wait for peers to announce.
func (c *Crawler) sampleLoop(ctx context.Context) {
	for {
		var addr krpc.NodeAddr
		select {
		case <-ctx.Done():
			return
		case addr = <-c.nodeQ:
		}
		c.sampleNode(ctx, addr)
	}
}

func (c *Crawler) sampleNode(ctx context.Context, na krpc.NodeAddr) {
	qctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()

	// The target is required by BEP 51 and is not cosmetic: it selects which
	// part of the ID space the node reports on, and its reply carries the
	// nodes closest to that target. Randomising it per query is what turns
	// repeated sampling into a walk instead of a re-read.
	res := c.server.Query(qctx, dht.NewAddr(na.UDP()), "sample_infohashes", dht.QueryInput{
		MsgArgs:      krpc.MsgArgs{ID: c.server.ID(), Target: randomID()},
		RateLimiting: dht.QueryRateLimiting{},
	})
	c.absorbNodes(res)

	if res.Err != nil || res.Reply.R == nil || res.Reply.R.Samples == nil {
		c.bump(&c.sampleErr)
		return
	}
	samples := *res.Reply.R.Samples
	c.bump(&c.sampled)
	c.addStat(&c.harvested, int64(len(samples)))
	for _, ih := range samples {
		c.push(ih)
	}
	c.scheduleResample(na, res.Reply.R.Interval)
}

// scheduleResample honours the node's advertised refresh interval, clamped so
// a hostile or broken value cannot park a node forever or turn it into a hot
// loop.
func (c *Crawler) scheduleResample(na krpc.NodeAddr, interval *int64) {
	d := defaultResample
	if interval != nil && *interval > 0 {
		d = time.Duration(*interval) * time.Second
	}
	d = min(max(d, minResample), maxResample)
	c.nodeMu.Lock()
	c.nodeNext[na.String()] = time.Now().Add(d)
	c.nodeMu.Unlock()
}

// absorbNodes feeds the nodes carried by a query reply back into the queue.
func (c *Crawler) absorbNodes(res dht.QueryResult) {
	if res.Reply.R == nil {
		return
	}
	for _, n := range res.Reply.R.Nodes {
		c.enqueueNode(n.Addr)
	}
	for _, n := range res.Reply.R.Nodes6 {
		c.enqueueNode(n.Addr)
	}
}

// enqueueNode offers a node to the samplers, dropping it when it is not due
// for a resample or the queue is full. Dropping is correct here: the routing
// table refill re-offers everything a few seconds later, and blocking would
// stall whichever query reply we are walking.
func (c *Crawler) enqueueNode(na krpc.NodeAddr) {
	if na.Port == 0 {
		return
	}
	key := na.String()
	now := time.Now()

	c.nodeMu.Lock()
	if next, ok := c.nodeNext[key]; ok && now.Before(next) {
		c.nodeMu.Unlock()
		return
	}
	if len(c.nodeNext) >= nodeTrackMax {
		// Cheap bound: forget everything rather than track insertion order.
		// Re-learning a node costs one extra query, so the reset is harmless
		// and keeps the map from growing without limit.
		c.nodeNext = make(map[string]time.Time, nodeTrackMax/2)
	}
	// Reserve the node now so the other samplers skip it while this query is
	// in flight; a successful sample overwrites this with the real interval.
	c.nodeNext[key] = now.Add(minResample)
	c.nodeMu.Unlock()

	select {
	case c.nodeQ <- na:
	default:
	}
}

func randomID() [20]byte {
	var id [20]byte
	rand.Read(id[:])
	return id
}

func (c *Crawler) bump(p *int64) { c.addStat(p, 1) }

func (c *Crawler) addStat(p *int64, n int64) {
	c.statMu.Lock()
	*p += n
	c.statMu.Unlock()
}

func (c *Crawler) randomKnown() ([20]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.known) == 0 {
		return [20]byte{}, false
	}
	return c.known[randIntn(len(c.known))], true
}

func randIntn(n int) int {
	var b [8]byte
	rand.Read(b[:])
	return int(uint64(b[0])|uint64(b[1])<<8|uint64(b[2])<<16|uint64(b[3])<<24) % n
}

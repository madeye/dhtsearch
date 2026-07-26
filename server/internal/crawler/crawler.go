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
	probeInterval = 20 * time.Second
)

// Config controls the crawler.
type Config struct {
	// Port is the UDP listen port; 0 picks a random one.
	Port int
	// Enabled=false disables the crawler entirely (offline environments).
	Enabled bool
	// Logger, nil for log.Default().
	Logger *log.Logger
}

// Crawler is a running (or disabled) DHT crawler.
type Crawler struct {
	enabled bool
	server  *dht.Server
	out     chan string
	cancel  context.CancelFunc
	logger  *log.Logger

	mu    sync.Mutex
	seen  map[[20]byte]struct{}
	ring  [][20]byte // FIFO eviction order for seen
	known [][20]byte // recent infohashes for active probing
}

// Start launches the crawler. With Enabled=false it returns a Crawler whose
// Infohashes channel is nil; all other methods remain safe to call.
func Start(cfg Config) (*Crawler, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	c := &Crawler{
		enabled: cfg.Enabled,
		logger:  logger,
		seen:    make(map[[20]byte]struct{}, seenCapacity),
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
	go c.bootstrap(ctx)
	go c.probeLoop(ctx)
	logger.Printf("crawler: DHT node listening on %s", conn.LocalAddr())
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

func (c *Crawler) bootstrap(ctx context.Context) {
	if _, err := c.server.BootstrapContext(ctx); err != nil && ctx.Err() == nil {
		c.logger.Printf("crawler: bootstrap: %v", err)
	}
}

// probeLoop periodically issues get_peers for recently seen infohashes and
// sample_infohashes (BEP 51) requests to random known nodes, harvesting any
// infohashes that come back.
func (c *Crawler) probeLoop(ctx context.Context) {
	t := time.NewTicker(probeInterval)
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
		addr := dht.NewAddr(node.Addr.UDP())

		// BEP 51: ask for a sample of the node's stored infohashes.
		if res := c.server.Query(ctx, addr, "sample_infohashes", dht.QueryInput{
			MsgArgs: krpc.MsgArgs{ID: c.server.ID()},
		}); res.Err == nil && res.Reply.R != nil && res.Reply.R.Samples != nil {
			for _, ih := range *res.Reply.R.Samples {
				c.push(ih)
			}
		}

		// Follow up on a recently seen infohash.
		if ih, ok := c.randomKnown(); ok {
			c.server.GetPeers(ctx, addr, int160.FromByteArray(ih), false, dht.QueryRateLimiting{})
		}
	}
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

package crawler

import (
	"net"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
)

func testCrawler() *Crawler {
	return &Crawler{
		seen:     make(map[[20]byte]struct{}),
		nodeQ:    make(chan krpc.NodeAddr, 8),
		nodeNext: make(map[string]time.Time),
	}
}

func addr(port int) krpc.NodeAddr {
	return krpc.NodeAddr{IP: net.IPv4(10, 0, 0, 1), Port: port}
}

func TestEnqueueNodeDedupsUntilDue(t *testing.T) {
	c := testCrawler()

	c.enqueueNode(addr(6881))
	if len(c.nodeQ) != 1 {
		t.Fatalf("queued=%d, want 1", len(c.nodeQ))
	}
	<-c.nodeQ

	// Re-offering the same node before its resample time is a no-op, so the
	// routing-table refill can rescan the whole table every few seconds
	// without the samplers re-querying nodes they just read.
	c.enqueueNode(addr(6881))
	if len(c.nodeQ) != 0 {
		t.Fatalf("queued=%d, want 0 (node not due yet)", len(c.nodeQ))
	}

	// Once due, it is offered again.
	c.nodeMu.Lock()
	c.nodeNext[addr(6881).String()] = time.Now().Add(-time.Second)
	c.nodeMu.Unlock()
	c.enqueueNode(addr(6881))
	if len(c.nodeQ) != 1 {
		t.Fatalf("queued=%d, want 1 (node became due)", len(c.nodeQ))
	}
}

func TestEnqueueNodeSkipsPortZero(t *testing.T) {
	c := testCrawler()
	c.enqueueNode(addr(0))
	if len(c.nodeQ) != 0 {
		t.Fatalf("queued=%d, want 0", len(c.nodeQ))
	}
}

func TestEnqueueNodeDropsWhenQueueFull(t *testing.T) {
	c := testCrawler()
	// Fill the queue past capacity; enqueue must never block.
	done := make(chan struct{})
	go func() {
		for p := 1; p <= 20; p++ {
			c.enqueueNode(addr(6000 + p))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueueNode blocked on a full queue")
	}
	if len(c.nodeQ) != cap(c.nodeQ) {
		t.Fatalf("queued=%d, want %d", len(c.nodeQ), cap(c.nodeQ))
	}
}

func TestScheduleResampleClampsInterval(t *testing.T) {
	c := testCrawler()
	sec := func(n int64) *int64 { return &n }

	cases := []struct {
		name     string
		interval *int64
		wantMin  time.Duration
		wantMax  time.Duration
	}{
		{"nil uses default", nil, defaultResample, defaultResample},
		{"tiny is floored", sec(1), minResample, minResample},
		{"huge is capped", sec(86400), maxResample, maxResample},
		{"sane is kept", sec(300), 5 * time.Minute, 5 * time.Minute},
		{"zero uses default", sec(0), defaultResample, defaultResample},
		{"negative uses default", sec(-5), defaultResample, defaultResample},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			na := addr(6881)
			before := time.Now()
			c.scheduleResample(na, tc.interval)

			c.nodeMu.Lock()
			next := c.nodeNext[na.String()]
			c.nodeMu.Unlock()

			d := next.Sub(before)
			if d < tc.wantMin-time.Second || d > tc.wantMax+time.Second {
				t.Fatalf("resample delay %v, want ~[%v,%v]", d, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestNodeTrackingIsBounded(t *testing.T) {
	c := testCrawler()
	c.nodeMu.Lock()
	for i := 0; i < nodeTrackMax; i++ {
		c.nodeNext[string(rune(i))] = time.Now().Add(time.Hour)
	}
	c.nodeMu.Unlock()

	c.enqueueNode(addr(6881))

	c.nodeMu.Lock()
	n := len(c.nodeNext)
	c.nodeMu.Unlock()
	if n >= nodeTrackMax {
		t.Fatalf("tracked=%d, want reset below %d", n, nodeTrackMax)
	}
}

func TestPushDedupesAndFillsChannel(t *testing.T) {
	c := testCrawler()
	c.out = make(chan string, 4)

	var ih [20]byte
	ih[0] = 0xAB
	c.push(ih)
	c.push(ih) // duplicate

	if len(c.out) != 1 {
		t.Fatalf("emitted=%d, want 1 (duplicate should be dropped)", len(c.out))
	}
	if got := <-c.out; got != "ab000000000000000000000000000000000000000"[:40] {
		t.Fatalf("hex=%q", got)
	}
}

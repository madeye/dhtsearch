// Package metadata fetches torrent metainfo (BEP 9) for discovered
// infohashes with a bounded worker pool, without downloading payload data.
package metadata

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/anacrolix/torrent"

	"dhtsearch/server/internal/filter"
)

// Record is the extracted metadata of one torrent.
type Record struct {
	InfoHash  string
	Name      string
	TotalSize int64
	FileCount int
	Files     []filter.File
}

// Config controls the fetcher.
type Config struct {
	// Workers is the size of the fetch worker pool. It also bounds the
	// number of concurrently active torrents in the client.
	Workers int
	// Timeout is how long to wait for one torrent's info before giving up.
	Timeout time.Duration
	// MaxFiles caps how many file entries are kept per torrent.
	MaxFiles int
	// Logger, nil for log.Default().
	Logger *log.Logger
}

// Fetcher consumes infohashes and reports successfully fetched metadata.
type Fetcher struct {
	cfg    Config
	client *torrent.Client
	tmpDir string
	logger *log.Logger

	wg     sync.WaitGroup
	cancel context.CancelFunc

	// Stats
	mu       sync.Mutex
	fetched  int64
	timedOut int64
	failed   int64
}

// NewFetcher creates a fetcher with its own torrent client. The client uses
// a temporary data dir and data download is disallowed per torrent, so no
// payload is persisted.
func NewFetcher(cfg Config) (*Fetcher, error) {
	if cfg.Workers <= 0 {
		cfg.Workers = 16
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = 50
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	tmpDir, err := os.MkdirTemp("", "dhtsearch-meta-*")
	if err != nil {
		return nil, err
	}
	tc := torrent.NewDefaultClientConfig()
	tc.DataDir = tmpDir
	tc.ListenPort = 0
	tc.NoUpload = true
	tc.Seed = false
	tc.NoDefaultPortForwarding = true
	client, err := torrent.NewClient(tc)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("torrent client: %w", err)
	}
	return &Fetcher{cfg: cfg, client: client, tmpDir: tmpDir, logger: logger}, nil
}

// Run starts the worker pool consuming hex infohashes from in until in is
// closed or the fetcher is closed. onRecord is called (from worker
// goroutines) for every torrent whose metadata was fetched in time.
func (f *Fetcher) Run(ctx context.Context, in <-chan string, onRecord func(Record)) {
	ctx, f.cancel = context.WithCancel(ctx)
	for i := 0; i < f.cfg.Workers; i++ {
		f.wg.Add(1)
		go f.worker(ctx, in, onRecord)
	}
}

func (f *Fetcher) worker(ctx context.Context, in <-chan string, onRecord func(Record)) {
	defer f.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case hexHash, ok := <-in:
			if !ok {
				return
			}
			if rec, ok := f.fetch(ctx, hexHash); ok {
				onRecord(rec)
			}
		}
	}
}

// fetch adds the magnet, waits for GotInfo with a timeout and always drops
// the torrent afterwards.
func (f *Fetcher) fetch(ctx context.Context, hexHash string) (Record, bool) {
	rec := Record{InfoHash: hexHash}
	t, err := f.client.AddMagnet("magnet:?xt=urn:btih:" + hexHash)
	if err != nil {
		f.count(&f.failed)
		return rec, false
	}
	defer t.Drop()
	t.DisallowDataDownload()

	select {
	case <-t.GotInfo():
	case <-time.After(f.cfg.Timeout):
		f.count(&f.timedOut)
		return rec, false
	case <-ctx.Done():
		return rec, false
	}

	info := t.Info()
	if info == nil {
		f.count(&f.failed)
		return rec, false
	}
	rec.Name = info.BestName()
	rec.TotalSize = info.TotalLength()
	files := info.UpvertedFiles()
	rec.FileCount = len(files)
	if n := len(files); n > f.cfg.MaxFiles {
		files = files[:f.cfg.MaxFiles]
	}
	for _, fi := range files {
		rec.Files = append(rec.Files, filter.File{
			Path: fi.DisplayPath(info),
			Size: fi.Length,
		})
	}
	f.count(&f.fetched)
	return rec, true
}

func (f *Fetcher) count(p *int64) {
	f.mu.Lock()
	*p++
	f.mu.Unlock()
}

// Stats returns fetched/timed-out/failed counters.
func (f *Fetcher) Stats() (fetched, timedOut, failed int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetched, f.timedOut, f.failed
}

// Close stops the workers and the torrent client.
func (f *Fetcher) Close() {
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()
	f.client.Close()
	os.RemoveAll(f.tmpDir)
}

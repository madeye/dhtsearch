// Command server runs the DHT search engine: DHT crawler, metadata workers
// and the HTTP search API.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"dhtsearch/server/internal/api"
	"dhtsearch/server/internal/crawler"
	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/store"
)

// envDefault returns the env value or fallback.
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func main() {
	logger := log.Default()

	addr := flag.String("addr", envDefault("HTTP_ADDR", ":8080"), "HTTP listen address")
	dbPath := flag.String("db", envDefault("DB_PATH", "./dhtsearch.db"), "SQLite database path")
	crawlEnabled := flag.Bool("crawl", envBool("CRAWL_ENABLED", true), "enable the DHT crawler")
	dhtPort := flag.Int("dht-port", envInt("DHT_PORT", 0), "DHT UDP listen port (0 = random)")
	metaWorkers := flag.Int("meta-workers", envInt("META_WORKERS", 16), "metadata fetch worker count")
	metaTimeout := flag.Duration("meta-timeout", envDuration("META_TIMEOUT", 45*time.Second), "metadata fetch timeout per torrent")
	fetchMetadata := flag.Bool("fetch-metadata", envBool("FETCH_METADATA", true), "fetch torrent metadata (false: store bare infohashes)")
	seedDemo := flag.Bool("seed-demo", false, "insert demo records at startup")
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		logger.Fatalf("store: %v", err)
	}
	defer st.Close()

	if *seedDemo {
		seed(st, logger)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Crawler.
	cr, err := crawler.Start(crawler.Config{
		Port:    *dhtPort,
		Enabled: *crawlEnabled,
		Logger:  logger,
	})
	if err != nil {
		logger.Fatalf("crawler: %v", err)
	}
	defer cr.Close()

	// Pipeline: infohashes -> metadata -> filter -> store.
	if *fetchMetadata {
		fetcher, err := metadata.NewFetcher(metadata.Config{
			Workers: *metaWorkers,
			Timeout: *metaTimeout,
			Logger:  logger,
		})
		if err != nil {
			logger.Fatalf("metadata: %v", err)
		}
		defer fetcher.Close()
		fetcher.Run(ctx, cr.Infohashes(), func(rec metadata.Record) {
			res := filter.Check(rec.Name, rec.Files, rec.TotalSize)
			if res.Adult {
				st.IncrStat("adult_filtered", 1)
				return
			}
			if res.Spam {
				st.IncrStat("spam_filtered", 1)
				return
			}
			if err := st.Upsert(store.Torrent{
				InfoHash:  rec.InfoHash,
				Name:      rec.Name,
				TotalSize: rec.TotalSize,
				FileCount: rec.FileCount,
				Files:     rec.Files,
				CreatedAt: time.Now().Unix(),
			}); err != nil {
				logger.Printf("store upsert: %v", err)
				return
			}
			st.IncrStat("fetched", 1)
		})
	} else {
		// Bare infohash collection: no metadata, no filter.
		go func() {
			for hash := range cr.Infohashes() {
				if err := st.Upsert(store.Torrent{
					InfoHash:  hash,
					Name:      hash,
					TotalSize: 1,
					CreatedAt: time.Now().Unix(),
				}); err != nil {
					logger.Printf("store upsert: %v", err)
					continue
				}
				st.IncrStat("seen", 1)
			}
		}()
	}

	// Mirror the crawler's in-memory seen count into the stats table
	// periodically.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				seen := int64(cr.SeenCount())
				st.IncrStat("seen", seen-prevSeen(&seen))
			}
		}
	}()

	// HTTP API.
	srv := &http.Server{
		Addr: *addr,
		Handler: api.New(st, func() api.CrawlerStatus {
			return api.CrawlerStatus{Enabled: cr.Enabled(), Seen: int64(cr.SeenCount())}
		}, logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	logger.Printf("http: listening on %s (db=%s crawl=%v fetch-metadata=%v)",
		*addr, *dbPath, *crawlEnabled, *fetchMetadata)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logger.Fatalf("http: %v", err)
	}
	logger.Printf("shutdown complete")
}

var lastSeen int64

// prevSeen returns the previously reported seen count and records the new
// one, so the stats mirror only applies deltas.
func prevSeen(cur *int64) int64 {
	prev := lastSeen
	lastSeen = *cur
	return prev
}

// seed inserts demo records so the API is testable without DHT access.
func seed(st *store.Store, logger *log.Logger) {
	type demo struct {
		hash, name string
		size       int64
		files      []filter.File
	}
	now := time.Now().Unix()
	demos := []demo{
		{"08ada5a7a6183aae1e09d831df6748d566095a10", "Sintel (2010) 1080p Open Movie", 867 << 20,
			[]filter.File{{Path: "Sintel.2010.1080p.mkv", Size: 867 << 20}}},
		{"56bc8614cf8d77448cb6ea12a2bd59fd28fa9348", "Big Buck Bunny (2008) 1080p", 712 << 20,
			[]filter.File{{Path: "BigBuckBunny_1080p.mp4", Size: 712 << 20}}},
		{"2c6b6858b61c9548d0b6e51a4a3f37adac0fe0b3", "Tears of Steel (2012) 4K Open Movie", 2 << 30,
			[]filter.File{{Path: "tears_of_steel_4k.mov", Size: 2 << 30}}},
		{"47f5e5f5e84d0f4a1d2b7f74b2cb24f2b4d2a0b1", "Ubuntu 24.04.1 LTS Desktop amd64 ISO", 5_900_000_000,
			[]filter.File{{Path: "ubuntu-24.04.1-desktop-amd64.iso", Size: 5_900_000_000}}},
		{"e2467cbf021192c241367b892230dc1e05c0580e", "Debian 12.7.0 amd64 netinst ISO", 660 << 20,
			[]filter.File{{Path: "debian-12.7.0-amd64-netinst.iso", Size: 660 << 20}}},
		{"3b245504cf5f11bbdbe1201cea6a6bf45aee1bc0", "Blender 4.2 LTS Windows/macOS/Linux Bundle", 380 << 20,
			[]filter.File{
				{Path: "blender-4.2.0-windows-x64.zip", Size: 190 << 20},
				{Path: "blender-4.2.0-macos-arm64.dmg", Size: 190 << 20},
			}},
		{"88594dbacbde6ef3e7eb30e4a11d0b00e0f02d1d", "NASA 4K Earth From Space Footage Collection", 12 << 30,
			[]filter.File{{Path: "earth_from_space_4k_ep1.mp4", Size: 4 << 30}}},
	}
	for i, d := range demos {
		if err := st.Upsert(store.Torrent{
			InfoHash:  d.hash,
			Name:      d.name,
			TotalSize: d.size,
			FileCount: len(d.files),
			Files:     d.files,
			CreatedAt: now - int64(len(demos)-i)*60,
		}); err != nil {
			logger.Printf("seed: %v", err)
		}
	}
	logger.Printf("seeded %d demo records", len(demos))
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return fallback
}

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
	"dhtsearch/server/internal/envfile"
	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/moderator"
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

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func main() {
	logger := log.Default()

	// Load .env before reading any env-backed default. Real environment
	// variables take precedence over the file.
	envPath := envDefault("ENV_FILE", ".env")
	if err := envfile.Load(envPath); err != nil {
		logger.Printf("env: %s: %v", envPath, err)
	}

	addr := flag.String("addr", envDefault("HTTP_ADDR", ":8080"), "HTTP listen address")
	dbPath := flag.String("db", envDefault("DB_PATH", "./dhtsearch.db"), "SQLite database path")
	crawlEnabled := flag.Bool("crawl", envBool("CRAWL_ENABLED", true), "enable the DHT crawler")
	dhtPort := flag.Int("dht-port", envInt("DHT_PORT", 0), "DHT UDP listen port (0 = random)")
	samplers := flag.Int("dht-samplers", envInt("DHT_SAMPLERS", 8), "concurrent BEP 51 sampling workers")
	metaWorkers := flag.Int("meta-workers", envInt("META_WORKERS", 128), "metadata fetch worker count")
	metaTimeout := flag.Duration("meta-timeout", envDuration("META_TIMEOUT", 45*time.Second), "metadata fetch timeout per torrent")
	fetchMetadata := flag.Bool("fetch-metadata", envBool("FETCH_METADATA", true), "fetch torrent metadata (false: store bare infohashes)")
	seedDemo := flag.Bool("seed-demo", false, "insert demo records at startup")
	minSize := flag.Int64("min-size", envInt64("MIN_TORRENT_SIZE", 100<<20),
		"skip torrents whose total size is below this many bytes")

	// LLM moderation pass.
	modEnabled := flag.Bool("moderate", envBool("MODERATION_ENABLED", true),
		"enable the periodic LLM moderation pass (requires OPENAI_API_KEY)")
	modBaseURL := flag.String("moderate-base-url", envDefault("OPENAI_BASE_URL", "https://openrouter.ai/api/v1"),
		"OpenAI-compatible API base URL")
	modModel := flag.String("moderate-model", envDefault("OPENAI_MODEL", "nvidia/nemotron-3-super-120b-a12b:free"),
		"chat model used for moderation")
	modInterval := flag.Duration("moderate-interval", envDuration("MODERATION_INTERVAL", time.Hour),
		"how often to run the moderation pass")
	modBatch := flag.Int("moderate-batch", envInt("MODERATION_BATCH_SIZE", 100),
		"torrent titles per moderation request")
	modMaxBatches := flag.Int("moderate-max-batches", envInt("MODERATION_MAX_BATCHES", 20),
		"max batches per moderation pass (0 = unlimited)")
	modDryRun := flag.Bool("moderate-dry-run", envBool("MODERATION_DRY_RUN", false),
		"log what moderation would delete without deleting it")
	modTrim := flag.Bool("moderate-trim-titles", envBool("MODERATION_TRIM_TITLES", true),
		"also strip advertising from torrent titles during the moderation pass")
	flag.Parse()

	filter.MinTotalSize = *minSize

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
		Port:     *dhtPort,
		Enabled:  *crawlEnabled,
		Samplers: *samplers,
		Logger:   logger,
	})
	if err != nil {
		logger.Fatalf("crawler: %v", err)
	}
	defer cr.Close()

	// Pipeline: infohashes -> metadata -> filter -> store.
	var fetcher *metadata.Fetcher
	if *fetchMetadata {
		var err error
		fetcher, err = metadata.NewFetcher(metadata.Config{
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
			if res.TooSmall {
				st.IncrStat("size_filtered", 1)
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

	// Periodic LLM moderation pass over rows the static filter admitted.
	if *modEnabled {
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			logger.Printf("moderator: disabled (OPENAI_API_KEY not set; see env.example)")
		} else {
			mod, err := moderator.New(st, moderator.Config{
				BaseURL:    *modBaseURL,
				APIKey:     key,
				Model:      *modModel,
				Interval:   *modInterval,
				BatchSize:  *modBatch,
				MaxBatches: *modMaxBatches,
				DryRun:     *modDryRun,
				TrimTitles: *modTrim,
				Logger:     logger,
			})
			if err != nil {
				logger.Fatalf("moderator: %v", err)
			}
			go mod.Run(ctx)
		}
	}

	// Mirror the crawler's in-memory seen count and the fetch outcome
	// counters into the stats table periodically. Both are process-local and
	// monotonic, so only the delta since the last tick is applied.
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
				if fetcher != nil {
					_, timedOut, failed := fetcher.Stats()
					st.IncrStat("meta_timed_out", timedOut-prevTimedOut(&timedOut))
					st.IncrStat("meta_failed", failed-prevFailed(&failed))
				}
			}
		}
	}()

	// HTTP API.
	srv := &http.Server{
		Addr: *addr,
		Handler: api.New(st, func() api.CrawlerStatus {
			cs := cr.Stats()
			return api.CrawlerStatus{
				Enabled:   cr.Enabled(),
				Seen:      int64(cr.SeenCount()),
				Nodes:     cs.Nodes,
				Queued:    cs.Queued,
				Sampled:   cs.Sampled,
				SampleErr: cs.SampleErr,
				Harvested: cs.Harvested,
			}
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

var (
	lastSeen     int64
	lastTimedOut int64
	lastFailed   int64
)

// prevSeen returns the previously reported seen count and records the new
// one, so the stats mirror only applies deltas.
func prevSeen(cur *int64) int64 {
	prev := lastSeen
	lastSeen = *cur
	return prev
}

// prevTimedOut and prevFailed do the same for the fetch outcome counters.
func prevTimedOut(cur *int64) int64 {
	prev := lastTimedOut
	lastTimedOut = *cur
	return prev
}

func prevFailed(cur *int64) int64 {
	prev := lastFailed
	lastFailed = *cur
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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"dunkirk.sh/tangle-of-trust/internal/db"
	"dunkirk.sh/tangle-of-trust/internal/resolve"
)

const (
	VouchCollection      = "sh.tangled.graph.vouch"
	FollowCollection     = "sh.tangled.graph.follow"
	KnotMemberCollection = "sh.tangled.knot.member"
	DefaultKnotDID       = "did:plc:wshs7t2adsemcrrd4snkeqli"
	ListRecordsLimit     = 100
	MaxWorkers           = 20
	ProfileBatchSize     = 50
	ProfileInterval      = 30 * time.Second
	MaxRetries           = 3
	RetryBaseDelay       = 2 * time.Second
)

var httpClient = &http.Client{
	Timeout: 15 * time.Second,
}

type ListRecordsResponse struct {
	Cursor  string `json:"cursor"`
	Records []struct {
		URI   string          `json:"uri"`
		CID   string          `json:"cid"`
		Value json.RawMessage `json:"value"`
	} `json:"records"`
}

type VouchRecord struct {
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"createdAt"`
}

type FollowRecord struct {
	Subject   string `json:"subject"`
	CreatedAt string `json:"createdAt"`
}

type KnotMemberRecord struct {
	CreatedAt string `json:"createdAt"`
}

func isValidDID(s string) bool {
	return strings.HasPrefix(s, "did:plc:") || strings.HasPrefix(s, "did:web:")
}

func main() {
	dbPath := flag.String("db", "tangle.db", "path to sqlite database")
	workers := flag.Int("workers", MaxWorkers, "number of parallel workers")
	knotDID := flag.String("knot", DefaultKnotDID, "default knot DID to seed members from")
	flag.Parse()
	args := flag.Args()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	store, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("interrupted, shutting down")
		cancel()
	}()

	seedDIDs := args
	if len(seedDIDs) == 0 {
		seedDIDs = bootstrapFromDB(store)
	}

	slog.Info("seeding from knot members", "knot", *knotDID)
	knotMembers, err := fetchKnotMembers(ctx, *knotDID)
	if err != nil {
		slog.Warn("failed to fetch knot members, continuing without", "error", err)
	} else {
		slog.Info("found knot members", "count", len(knotMembers))
		for _, km := range knotMembers {
			seedDIDs = append(seedDIDs, km.MemberDID)
			if err := store.UpsertKnotMember(km); err != nil {
				slog.Warn("failed to store knot member", "error", err)
			}
		}
	}

	if len(seedDIDs) == 0 {
		slog.Error("no seed DIDs; pass DIDs as arguments or ensure knot is reachable")
		os.Exit(1)
	}

	dedup := make(map[string]bool)
	cleanSeeds := make([]string, 0, len(seedDIDs))
	for _, d := range seedDIDs {
		if !dedup[d] && isValidDID(d) {
			dedup[d] = true
			cleanSeeds = append(cleanSeeds, d)
		}
	}

	slog.Info("starting snowball backfill", "seeds", len(cleanSeeds), "workers", *workers)

	pdsCache := &sync.Map{}

	visited := sync.Map{}
	didCh := make(chan string, 10000)
	var inFlight sync.WaitGroup

	for _, did := range cleanSeeds {
		visited.Store(did, true)
		inFlight.Add(1)
		didCh <- did
	}

	var totalVisited atomic.Int64
	var totalVouches atomic.Int64
	var totalFollows atomic.Int64
	var totalErrors atomic.Int64
	var startTime = time.Now()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				v := totalVisited.Load()
				elapsed := time.Since(startTime).Round(time.Second)
				rate := float64(v) / elapsed.Seconds()
				slog.Info("progress",
					"visited", v,
					"vouches", totalVouches.Load(),
					"follows", totalFollows.Load(),
					"errors", totalErrors.Load(),
					"queue", len(didCh),
					"rate", fmt.Sprintf("%.1f/s", rate),
					"elapsed", elapsed.String(),
				)
			}
		}
	}()

	// background profile enrichment
	var profileWg sync.WaitGroup
	profileDIDs := make(chan string, 5000)
	profileWg.Add(1)
	go func() {
		defer profileWg.Done()
		batch := make([]string, 0, ProfileBatchSize)
		timer := time.NewTimer(ProfileInterval)
		defer timer.Stop()

		flush := func() {
			if len(batch) == 0 {
				return
			}
			dids := make([]string, len(batch))
			copy(dids, batch)
			batch = batch[:0]

			enriched, err := resolve.BatchResolveAndStore(ctx, store, dids)
			if err != nil {
				slog.Warn("batch profile resolve had errors", "count", len(dids), "error", err)
			}
			if enriched > 0 {
				slog.Info("enriched profiles", "count", enriched)
			}
		}

		for {
			select {
			case <-ctx.Done():
				flush()
				return
			case did, ok := <-profileDIDs:
				if !ok {
					flush()
					return
				}
				batch = append(batch, did)
				if len(batch) >= ProfileBatchSize {
					flush()
					timer.Reset(ProfileInterval)
				}
			case <-timer.C:
				flush()
				timer.Reset(ProfileInterval)
			}
		}
	}()

	go func() {
		inFlight.Wait()
		close(didCh)
	}()

	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for did := range didCh {
				select {
				case <-ctx.Done():
					inFlight.Done()
					return
				default:
				}

				totalVisited.Add(1)

				newDIDs, err := backfillDID(ctx, store, pdsCache, did, &totalVouches, &totalFollows)
				if err != nil {
					totalErrors.Add(1)
					slog.Warn("backfill failed", "did", did, "error", err)
					inFlight.Done()
					continue
				}

				for _, newDID := range newDIDs {
					if !isValidDID(newDID) {
						continue
					}
					if _, loaded := visited.LoadOrStore(newDID, true); !loaded {
						inFlight.Add(1)
						select {
						case didCh <- newDID:
							// queue new DID for profile enrichment
							select {
							case profileDIDs <- newDID:
							default:
							}
						case <-ctx.Done():
							inFlight.Done()
						}
					}
				}
				inFlight.Done()
			}
		}(i)
	}

	wg.Wait()
	close(profileDIDs)
	profileWg.Wait()

	elapsed := time.Since(startTime).Round(time.Second)
	rate := float64(totalVisited.Load()) / elapsed.Seconds()
	slog.Info("backfill complete",
		"visited", totalVisited.Load(),
		"vouches", totalVouches.Load(),
		"follows", totalFollows.Load(),
		"errors", totalErrors.Load(),
		"elapsed", elapsed.String(),
		"rate", fmt.Sprintf("%.1f/s", rate),
	)
}

func bootstrapFromDB(store *db.Store) []string {
	dids, err := store.AllDIDs()
	if err != nil || len(dids) == 0 {
		return nil
	}
	return dids
}

func fetchKnotMembers(ctx context.Context, knotDID string) ([]db.KnotMember, error) {
	pdsURL, err := cachedResolvePDS(ctx, nil, knotDID)
	if err != nil {
		return nil, fmt.Errorf("resolve PDS for knot: %w", err)
	}

	var members []db.KnotMember
	cursor := ""

	for {
		select {
		case <-ctx.Done():
			return members, ctx.Err()
		default:
		}

		records, nextCursor, err := listRecordsWithRetry(ctx, pdsURL, knotDID, KnotMemberCollection, cursor)
		if err != nil {
			return members, err
		}

		for _, rec := range records {
			rkey := extractRkey(rec.URI)
			if rkey == "" {
				continue
			}

			var kmRec KnotMemberRecord
			json.Unmarshal(rec.Value, &kmRec)

			createdAt := kmRec.CreatedAt
			updatedAt := time.Now()
			if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
				updatedAt = t
			}

			members = append(members, db.KnotMember{
				KnotDID:   knotDID,
				MemberDID: rkey,
				Rkey:      rkey,
				CreatedAt: createdAt,
				UpdatedAt: updatedAt,
			})
		}

		if nextCursor == "" || len(records) == 0 {
			break
		}
		cursor = nextCursor
	}

	return members, nil
}

func backfillDID(ctx context.Context, store *db.Store, pdsCache *sync.Map, did string, vouchCount, followCount *atomic.Int64) ([]string, error) {
	pdsURL, err := cachedResolvePDS(ctx, pdsCache, did)
	if err != nil {
		return nil, fmt.Errorf("resolve PDS: %w", err)
	}

	var newDIDs []string

	type result struct {
		dids  []string
		count int
		err   error
	}

	vouchCh := make(chan result, 1)
	followCh := make(chan result, 1)

	go func() {
		dids, count, err := fetchVouches(ctx, store, pdsURL, did)
		vouchCh <- result{dids, count, err}
	}()

	go func() {
		dids, count, err := fetchFollows(ctx, store, pdsURL, did)
		followCh <- result{dids, count, err}
	}()

	vr := <-vouchCh
	if vr.err != nil {
		return newDIDs, fmt.Errorf("fetch vouches: %w", vr.err)
	}
	vouchCount.Add(int64(vr.count))
	newDIDs = append(newDIDs, vr.dids...)

	fr := <-followCh
	if fr.err != nil {
		slog.Warn("fetch follows failed", "did", did, "error", fr.err)
	} else {
		followCount.Add(int64(fr.count))
		newDIDs = append(newDIDs, fr.dids...)
	}

	return newDIDs, nil
}

func fetchVouches(ctx context.Context, store *db.Store, pdsURL, did string) ([]string, int, error) {
	var newDIDs []string
	var vouches []db.Vouch
	cursor := ""

	for {
		records, nextCursor, err := listRecordsWithRetry(ctx, pdsURL, did, VouchCollection, cursor)
		if err != nil {
			return newDIDs, 0, err
		}

		for _, rec := range records {
			var vouch VouchRecord
			if err := json.Unmarshal(rec.Value, &vouch); err != nil {
				continue
			}

			rkey := extractRkey(rec.URI)
			if rkey == "" {
				continue
			}

			updatedAt := time.Now()
			if t, err := time.Parse(time.RFC3339Nano, vouch.CreatedAt); err == nil {
				updatedAt = t
			}

			vouches = append(vouches, db.Vouch{
				VoucherDID: did,
				VoucheeDID: rkey,
				Kind:      vouch.Kind,
				Reason:    vouch.Reason,
				CreatedAt: vouch.CreatedAt,
				Seq:       0,
				UpdatedAt: updatedAt,
			})
			if isValidDID(rkey) {
				newDIDs = append(newDIDs, rkey)
			}
		}

		if nextCursor == "" || len(records) == 0 {
			break
		}
		cursor = nextCursor
	}

	if len(vouches) > 0 {
		if err := store.BatchUpsertVouches(vouches); err != nil {
			return newDIDs, 0, fmt.Errorf("batch upsert vouches: %w", err)
		}
	}

	return newDIDs, len(vouches), nil
}

func fetchFollows(ctx context.Context, store *db.Store, pdsURL, did string) ([]string, int, error) {
	var newDIDs []string
	var follows []db.Follow
	cursor := ""

	for {
		records, nextCursor, err := listRecordsWithRetry(ctx, pdsURL, did, FollowCollection, cursor)
		if err != nil {
			return newDIDs, 0, err
		}

		for _, rec := range records {
			var follow FollowRecord
			if err := json.Unmarshal(rec.Value, &follow); err != nil {
				continue
			}

			if follow.Subject == "" || !isValidDID(follow.Subject) {
				continue
			}

			updatedAt := time.Now()
			if t, err := time.Parse(time.RFC3339Nano, follow.CreatedAt); err == nil {
				updatedAt = t
			}

			follows = append(follows, db.Follow{
				ActorDID:   did,
				SubjectDID: follow.Subject,
				CreatedAt:  follow.CreatedAt,
				UpdatedAt:  updatedAt,
			})
			newDIDs = append(newDIDs, follow.Subject)
		}

		if nextCursor == "" || len(records) == 0 {
			break
		}
		cursor = nextCursor
	}

	if len(follows) > 0 {
		if err := store.BatchUpsertFollows(follows); err != nil {
			return newDIDs, 0, fmt.Errorf("batch upsert follows: %w", err)
		}
	}

	return newDIDs, len(follows), nil
}

type Record struct {
	URI   string
	Value json.RawMessage
}

func listRecordsWithRetry(ctx context.Context, pdsURL, did, collection, cursor string) ([]Record, string, error) {
	var lastErr error
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		if attempt > 0 {
			delay := RetryBaseDelay * time.Duration(math.Pow(2, float64(attempt-1)))
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(delay):
			}
		}
		records, nextCursor, err := listRecords(ctx, pdsURL, did, collection, cursor)
		if err == nil {
			return records, nextCursor, nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

func listRecords(ctx context.Context, pdsURL, did, collection, cursor string) ([]Record, string, error) {
	u := fmt.Sprintf("%s/xrpc/com.atproto.repo.listRecords?repo=%s&collection=%s&limit=%d",
		pdsURL, did, collection, ListRecordsLimit)
	if cursor != "" {
		u += "&cursor=" + cursor
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, "", nil
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("listRecords %s returned %d: %s", collection, resp.StatusCode, truncate(string(body), 200))
	}

	var result ListRecordsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", err
	}

	records := make([]Record, len(result.Records))
	for i, r := range result.Records {
		records[i] = Record{URI: r.URI, Value: r.Value}
	}

	return records, result.Cursor, nil
}

func cachedResolvePDS(ctx context.Context, cache *sync.Map, did string) (string, error) {
	if cache != nil {
		if v, ok := cache.Load(did); ok {
			return v.(string), nil
		}
	}

	pdsURL, err := resolvePDS(ctx, did)
	if err != nil {
		return "", err
	}

	if cache != nil {
		cache.Store(did, pdsURL)
	}
	return pdsURL, nil
}

func resolvePDS(ctx context.Context, did string) (string, error) {
	if strings.HasPrefix(did, "did:web:") {
		return "https://" + strings.TrimPrefix(did, "did:web:"), nil
	}

	if !strings.HasPrefix(did, "did:plc:") {
		return "", fmt.Errorf("unsupported DID: %s", did)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://plc.directory/"+did, nil)
	if err != nil {
		return "", err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("plc lookup returned %d", resp.StatusCode)
	}

	var doc struct {
		Service []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			URL  string `json:"serviceEndpoint"`
		} `json:"service"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", err
	}

	for _, s := range doc.Service {
		if s.Type == "AtmospherePds" || s.ID == "#atproto_pds" {
			return strings.TrimRight(s.URL, "/"), nil
		}
	}

	return "", fmt.Errorf("no PDS service found for %s", did)
}

func extractRkey(uri string) string {
	parts := strings.Split(uri, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dunkirk.sh/tangle-of-trust/internal/db"
)

const (
	PlcURL        = "https://plc.directory"
	BskyPublicAPI = "https://public.api.bsky.app"
	MaxBatchSize  = 25
)

var httpClient = &http.Client{
	Timeout: 15 * time.Second,
}

func isValidDID(s string) bool {
	return strings.HasPrefix(s, "did:plc:") || strings.HasPrefix(s, "did:web:")
}

type PlcDoc struct {
	AlsoKnownAs []string `json:"alsoKnownAs"`
}

type BskyProfile struct {
	DID    string `json:"did"`
	Handle string `json:"handle"`
	Avatar string `json:"avatar"`
}

func ResolveHandle(ctx context.Context, did string) (string, error) {
	if strings.HasPrefix(did, "did:web:") {
		return strings.TrimPrefix(did, "did:web:"), nil
	}

	if !strings.HasPrefix(did, "did:plc:") {
		return "", fmt.Errorf("unsupported DID: %s", did)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", PlcURL+"/"+did, nil)
	if err != nil {
		return "", err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("plc returned %d", resp.StatusCode)
	}

	var doc PlcDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", err
	}

	for _, aka := range doc.AlsoKnownAs {
		if strings.HasPrefix(aka, "at://") {
			return strings.TrimPrefix(aka, "at://"), nil
		}
		// accept bare handles (contain a dot, no slashes)
		if strings.Contains(aka, ".") && !strings.Contains(aka, "/") {
			return aka, nil
		}
	}

	return "", fmt.Errorf("no handle found")
}

func BatchProfiles(ctx context.Context, dids []string) ([]BskyProfile, error) {
	if len(dids) == 0 {
		return nil, nil
	}

	if len(dids) > MaxBatchSize {
		dids = dids[:MaxBatchSize]
	}

	// build URL with repeated actors params: actors=did:plc:xxx&actors=did:plc:yyy
	params := url.Values{}
	for _, did := range dids {
		params.Add("actors", did)
	}

	u := fmt.Sprintf("%s/xrpc/app.bsky.actor.getProfiles?%s", BskyPublicAPI, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getProfiles returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result struct {
		Profiles []BskyProfile `json:"profiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Profiles, nil
}

func ResolveAndStore(ctx context.Context, store *db.Store, did string) error {
	if !isValidDID(did) {
		return fmt.Errorf("invalid DID: %s", did)
	}

	// Try BatchProfiles first — one call gives handle + avatar
	var handle, avatarURL string
	profiles, err := BatchProfiles(ctx, []string{did})
	if err != nil {
		slog.Warn("batch profile fetch failed, falling back to PLC", "did", did, "error", err)
	}
	if len(profiles) > 0 && profiles[0].Handle != "" {
		handle = profiles[0].Handle
		avatarURL = profiles[0].Avatar
	} else {
		// PLC fallback for handle only
		h, err := ResolveHandle(ctx, did)
		if err != nil {
			return fmt.Errorf("resolve handle: %w", err)
		}
		handle = h
	}

	p := db.Profile{
		DID:       did,
		Handle:    handle,
		AvatarURL: avatarURL,
		UpdatedAt: time.Now(),
	}
	return store.UpsertProfile(p)
}

func BatchResolveAndStore(ctx context.Context, store *db.Store, dids []string) (int, error) {
	if len(dids) == 0 {
		return 0, nil
	}

	// filter to valid DIDs only
	validDIDs := make([]string, 0, len(dids))
	for _, d := range dids {
		if isValidDID(d) {
			validDIDs = append(validDIDs, d)
		}
	}

	if len(validDIDs) == 0 {
		return 0, nil
	}

	// resolve handles via PLC in parallel-ish (batch the getProfiles for avatars)
	profiles, err := BatchProfiles(ctx, validDIDs)
	if err != nil {
		slog.Warn("batch profile fetch failed, falling back to individual PLC resolution", "error", err)
		profiles = nil
	}

	profileMap := make(map[string]*BskyProfile, len(profiles))
	for i := range profiles {
		profileMap[profiles[i].DID] = &profiles[i]
	}

	count := 0
	for _, did := range validDIDs {
		p := db.Profile{
			DID:       did,
			UpdatedAt: time.Now(),
		}

		if bp, ok := profileMap[did]; ok {
			p.Handle = bp.Handle
			p.AvatarURL = bp.Avatar
		} else {
			// fallback: resolve handle from PLC
			handle, err := ResolveHandle(ctx, did)
			if err != nil {
				slog.Warn("handle resolution failed", "did", did, "error", err)
			} else {
				p.Handle = handle
			}
		}

		if p.Handle == "" && p.AvatarURL == "" {
			continue
		}

		if err := store.UpsertProfile(p); err != nil {
			slog.Error("failed to upsert profile", "did", did, "error", err)
			continue
		}
		count++
	}

	return count, nil
}

func EnrichMissing(ctx context.Context, store *db.Store, batchSize int) (int, error) {
	total := 0
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}

		dids, err := store.ProfilesNeedingResolution(batchSize)
		if err != nil {
			return total, fmt.Errorf("query DIDs: %w", err)
		}

		// filter to valid DIDs
		validDIDs := make([]string, 0, len(dids))
		for _, d := range dids {
			if isValidDID(d) {
				validDIDs = append(validDIDs, d)
			}
		}

		if len(validDIDs) == 0 {
			return total, nil
		}

		// process in batches of MaxBatchSize
		for i := 0; i < len(validDIDs); i += MaxBatchSize {
			end := i + MaxBatchSize
			if end > len(validDIDs) {
				end = len(validDIDs)
			}
			batch := validDIDs[i:end]

			count, err := BatchResolveAndStore(ctx, store, batch)
			if err != nil {
				slog.Warn("batch failed", "error", err)
			}
			total += count
		}

		slog.Info("enriched profiles", "batch_size", len(validDIDs), "total", total)

		if len(validDIDs) < batchSize {
			return total, nil
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

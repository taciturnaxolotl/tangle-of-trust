package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"dunkirk.sh/tangle-of-trust/internal/db"
	"dunkirk.sh/tangle-of-trust/internal/resolve"
)

type GraphNode struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Handle    string `json:"handle,omitempty"`
	AvatarURL string `json:"avatar,omitempty"`
}

type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Time   string `json:"time"`
}

type GraphData struct {
	Nodes    []GraphNode `json:"nodes"`
	Edges    []GraphEdge `json:"edges"`
	Profiles map[string]GraphNode `json:"profiles,omitempty"`
	Stats    struct {
		Total     int64 `json:"total"`
		Vouches   int64 `json:"vouches"`
		Denounces int64 `json:"denounces"`
		Follows   int64 `json:"follows"`
		Knot      int64 `json:"knot"`
	} `json:"stats"`
	TimeRange struct {
		Min string `json:"min"`
		Max string `json:"max"`
	} `json:"timeRange"`
}

type Server struct {
	store *db.Store
	mux   *http.ServeMux
}

func NewServer(store *db.Store) *Server {
	s := &Server{
		store: store,
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/proxy/avatar", s.handleAvatarProxy)
	s.mux.HandleFunc("/api/search", s.handleSearch)
	s.mux.HandleFunc("/api/graph", s.handleGraph)
	s.mux.HandleFunc("/api/stats", s.handleStats)
	s.mux.HandleFunc("/api/resolve", s.handleResolve)
	s.mux.HandleFunc("/api/profiles", s.handleProfiles)
	s.mux.HandleFunc("/", s.handleIndex)
}

func (s *Server) Serve(addr string) error {
	slog.Info("web server starting", "addr", addr)
	return http.ListenAndServe(addr, s.mux)
}

func (s *Server) handleAvatarProxy(w http.ResponseWriter, r *http.Request) {
	avatarURL := r.URL.Query().Get("url")
	if avatarURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", avatarURL, nil)
	if err != nil {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		http.Error(w, fmt.Sprintf("upstream returned %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/png"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	io.Copy(w, resp.Body)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, map[string]interface{}{"actors": nil})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	// Always try Bluesky search first
	type SearchActor struct {
		DID    string `json:"did"`
		Handle string `json:"handle"`
		Avatar string `json:"avatar"`
	}
	var actors []SearchActor

	u := fmt.Sprintf("%s/xrpc/app.bsky.actor.searchActors?q=%s&limit=6", resolve.BskyPublicAPI, url.QueryEscape(q))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err == nil {
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			var result struct {
				Actors []SearchActor `json:"actors"`
			}
			if json.NewDecoder(resp.Body).Decode(&result) == nil {
				actors = result.Actors
			}
		} else if resp != nil {
			resp.Body.Close()
		}
	}

	// If Bluesky found nothing and query looks like a handle (contains a dot), resolve via ATProto
	if len(actors) == 0 && strings.Contains(q, ".") {
		did, err := resolve.ResolveDIDFromHandle(ctx, q)
		if err == nil && did != "" {
			// try to get avatar from Bluesky
			avatar := ""
			handle := q
			profiles, perr := resolve.BatchProfiles(ctx, []string{did})
			if perr == nil && len(profiles) > 0 {
				if profiles[0].Handle != "" {
					handle = profiles[0].Handle
				}
				avatar = profiles[0].Avatar
			}
			actors = append(actors, SearchActor{DID: did, Handle: handle, Avatar: avatar})
		}
	}

	writeJSON(w, map[string]interface{}{"actors": actors})
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	vouches, err := s.store.AllVouches()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	follows, _ := s.store.AllFollows()
	writeGraphJSON(w, vouches, follows, s.store)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	did := r.URL.Query().Get("did")
	if did == "" {
		http.Error(w, "missing did parameter", http.StatusBadRequest)
		return
	}

	// check if we already have it
	profile, err := s.store.GetProfile(did)
	if err == nil && profile.Handle != "" {
		writeJSON(w, profile)
		return
	}

	// resolve on demand
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := resolve.ResolveAndStore(ctx, s.store, did); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	profile, err = s.store.GetProfile(did)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, profile)
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.store.AllProfiles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, profiles)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	total, vouches, denounces, followCount, knotCount, err := s.store.Stats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]int64{
		"total":     total,
		"vouches":   vouches,
		"denounces": denounces,
		"follows":   followCount,
		"knot":      knotCount,
	})
}

func (s *Server) handleTimeRange(w http.ResponseWriter, r *http.Request) {
	min, max, err := s.store.TimeRange()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{
		"min": min.Format(time.RFC3339),
		"max": max.Format(time.RFC3339),
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "ui not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func writeGraphJSON(w http.ResponseWriter, vouches []db.Vouch, follows []db.Follow, store *db.Store) {
	nodeSet := make(map[string]bool)
	var edges []GraphEdge

	for _, v := range vouches {
		nodeSet[v.VoucherDID] = true
		nodeSet[v.VoucheeDID] = true
		edges = append(edges, GraphEdge{
			Source: v.VoucherDID,
			Target: v.VoucheeDID,
			Kind:   "vouch/" + v.Kind,
			Reason: v.Reason,
			Time:   v.UpdatedAt.Format(time.RFC3339),
		})
	}

	for _, f := range follows {
		nodeSet[f.ActorDID] = true
		nodeSet[f.SubjectDID] = true
		edges = append(edges, GraphEdge{
			Source: f.ActorDID,
			Target: f.SubjectDID,
			Kind:   "follow",
			Time:   f.UpdatedAt.Format(time.RFC3339),
		})
	}

	profiles, _ := store.AllProfiles()

	nodes := make([]GraphNode, 0, len(nodeSet))
	for id := range nodeSet {
		node := GraphNode{ID: id, Label: shortenDID(id)}
		if p, ok := profiles[id]; ok {
			node.Handle = p.Handle
			node.AvatarURL = p.AvatarURL
		}
		nodes = append(nodes, node)
	}

	total, vouchCount, denounceCount, followCount, knotCount, _ := store.Stats()
	minT, maxT, _ := store.TimeRange()

	gd := GraphData{
		Nodes: nodes,
		Edges: edges,
	}
	gd.Stats.Total = total
	gd.Stats.Vouches = vouchCount
	gd.Stats.Denounces = denounceCount
	gd.Stats.Follows = followCount
	gd.Stats.Knot = knotCount
	gd.TimeRange.Min = minT.Format(time.RFC3339)
	gd.TimeRange.Max = maxT.Format(time.RFC3339)

	writeJSON(w, gd)
}

func shortenDID(did string) string {
	if len(did) > 24 {
		return did[:12] + "…" + did[len(did)-8:]
	}
	return did
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func StartServer(ctx context.Context, addr string, store *db.Store) error {
	srv := NewServer(store)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	slog.Info("web server starting", "addr", addr)
	return httpServer.ListenAndServe()
}

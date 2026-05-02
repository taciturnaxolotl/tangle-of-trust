package jetstream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"dunkirk.sh/tangle-of-trust/internal/db"
)

const (
	VouchCollection     = "sh.tangled.graph.vouch"
	FollowCollection    = "sh.tangled.graph.follow"
	KnotMemberCollection = "sh.tangled.knot.member"
	DefaultURL          = "wss://jetstream2.us-east.bsky.network/subscribe"
)

type JetstreamEvent struct {
	Did      string          `json:"did"`
	TimeUS   int64           `json:"time_us"`
	Kind     string          `json:"kind"`
	Commit   json.RawMessage `json:"commit"`
	Identity json.RawMessage `json:"identity"`
}

type CommitData struct {
	Rev        string `json:"rev"`
	Operation  string `json:"operation"`
	Collection  string `json:"collection"`
	Rkey        string `json:"rkey"`
	Record      json.RawMessage `json:"record"`
	CID         string `json:"cid"`
}

type IdentityData struct {
	Did    string `json:"did"`
	Handle string `json:"handle"`
	Seq    int64  `json:"seq"`
	Time   string `json:"time"`
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

type Subscriber struct {
	url   string
	store *db.Store
	conn  *websocket.Conn
	mu    sync.Mutex
	cursor int64
}

func NewSubscriber(url string, store *db.Store) *Subscriber {
	return &Subscriber{
		url:   url,
		store: store,
	}
}

func (s *Subscriber) Run(ctx context.Context) error {
	cursor, err := s.store.LoadCursor()
	if err != nil {
		return fmt.Errorf("load cursor: %w", err)
	}
	s.cursor = cursor

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := s.connect(ctx); err != nil {
			slog.Error("connection failed, retrying", "error", err)
			time.Sleep(5 * time.Second)
			continue
		}

		slog.Info("connected to jetstream", "cursor", s.cursor)

		if err := s.consume(ctx); err != nil {
			slog.Error("consume error, reconnecting", "error", err)
			s.close()
			time.Sleep(2 * time.Second)
		}
	}
}

func (s *Subscriber) connect(ctx context.Context) error {
	u := s.url + "?wantedCollections=" + VouchCollection +
		"&wantedCollections=" + FollowCollection +
		"&wantedCollections=" + KnotMemberCollection
	if s.cursor > 0 {
		u += "&cursor=" + fmt.Sprintf("%d", s.cursor)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, u, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	return nil
}

func (s *Subscriber) close() {
	s.mu.Lock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	s.mu.Unlock()
}

func (s *Subscriber) consume(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()

		if conn == nil {
			return fmt.Errorf("connection lost")
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		if len(msg) == 0 {
			continue
		}

		var evt JetstreamEvent
		if err := json.Unmarshal(msg, &evt); err != nil {
			slog.Debug("skipping non-event message", "error", err)
			continue
		}

		switch evt.Kind {
		case "commit":
			if len(evt.Commit) == 0 {
				continue
			}
			var commit CommitData
			if err := json.Unmarshal(evt.Commit, &commit); err != nil {
				slog.Warn("failed to parse commit", "error", err)
				continue
			}
			eventTime := time.UnixMicro(evt.TimeUS)
			switch commit.Collection {
			case VouchCollection:
				s.handleVouch(evt, commit, eventTime)
			case FollowCollection:
				s.handleFollow(evt, commit, eventTime)
			case KnotMemberCollection:
				s.handleKnotMember(evt, commit, eventTime)
			}
		case "identity":
			if len(evt.Identity) == 0 {
				continue
			}
			s.handleIdentity(evt)
		}

		if evt.TimeUS > s.cursor {
			s.cursor = evt.TimeUS
			if err := s.store.SaveCursor(s.cursor); err != nil {
				slog.Error("failed to save cursor", "error", err)
			}
		}
	}
}

func (s *Subscriber) handleIdentity(evt JetstreamEvent) {
	var ident IdentityData
	if err := json.Unmarshal(evt.Identity, &ident); err != nil {
		slog.Warn("failed to parse identity", "error", err)
		return
	}

	if ident.Did == "" || ident.Handle == "" {
		return
	}

	p := db.Profile{
		DID:       ident.Did,
		Handle:    ident.Handle,
		UpdatedAt: time.Now(),
	}

	if err := s.store.UpsertProfile(p); err != nil {
		slog.Error("failed to upsert identity profile", "error", err)
		return
	}

	slog.Info("identity updated", "did", ident.Did, "handle", ident.Handle)
}

func (s *Subscriber) handleVouch(evt JetstreamEvent, commit CommitData, eventTime time.Time) {
	switch commit.Operation {
	case "create", "update":
		if len(commit.Record) == 0 {
			return
		}

		var rec VouchRecord
		if err := json.Unmarshal(commit.Record, &rec); err != nil {
			slog.Warn("failed to parse vouch record", "error", err, "did", evt.Did)
			return
		}

		v := db.Vouch{
			VoucherDID: evt.Did,
			VoucheeDID: commit.Rkey,
			Kind:      rec.Kind,
			Reason:    rec.Reason,
			CreatedAt: rec.CreatedAt,
			Seq:       evt.TimeUS,
			UpdatedAt: eventTime,
		}

		if err := s.store.UpsertVouch(v); err != nil {
			slog.Error("failed to upsert vouch", "error", err)
			return
		}

		slog.Info("vouch upserted", "voucher", evt.Did, "vouchee", commit.Rkey, "kind", rec.Kind)

	case "delete":
		if err := s.store.DeleteVouch(evt.Did, commit.Rkey); err != nil {
			slog.Error("failed to delete vouch", "error", err)
			return
		}
		slog.Info("vouch deleted", "voucher", evt.Did, "vouchee", commit.Rkey)
	}
}

func (s *Subscriber) handleFollow(evt JetstreamEvent, commit CommitData, eventTime time.Time) {
	switch commit.Operation {
	case "create", "update":
		if len(commit.Record) == 0 {
			return
		}

		var rec FollowRecord
		if err := json.Unmarshal(commit.Record, &rec); err != nil {
			slog.Warn("failed to parse follow record", "error", err, "did", evt.Did)
			return
		}

		f := db.Follow{
			ActorDID:   evt.Did,
			SubjectDID: rec.Subject,
			CreatedAt:  rec.CreatedAt,
			UpdatedAt:  eventTime,
		}

		if err := s.store.UpsertFollow(f); err != nil {
			slog.Error("failed to upsert follow", "error", err)
			return
		}

		slog.Info("follow upserted", "actor", evt.Did, "subject", rec.Subject)

	case "delete":
		if err := s.store.DeleteFollow(evt.Did, commit.Rkey); err != nil {
			slog.Error("failed to delete follow", "error", err)
			return
		}
		slog.Info("follow deleted", "actor", evt.Did, "rkey", commit.Rkey)
	}
}

func (s *Subscriber) handleKnotMember(evt JetstreamEvent, commit CommitData, eventTime time.Time) {
	switch commit.Operation {
	case "create", "update":
		if len(commit.Record) == 0 {
			return
		}

		var rec KnotMemberRecord
		if err := json.Unmarshal(commit.Record, &rec); err != nil {
			slog.Warn("failed to parse knot member record", "error", err, "did", evt.Did)
			return
		}

		km := db.KnotMember{
			KnotDID:   evt.Did,
			MemberDID: commit.Rkey,
			Rkey:      commit.Rkey,
			CreatedAt: rec.CreatedAt,
			UpdatedAt: eventTime,
		}

		if err := s.store.UpsertKnotMember(km); err != nil {
			slog.Error("failed to upsert knot member", "error", err)
			return
		}

		slog.Info("knot member upserted", "knot", evt.Did, "member", commit.Rkey)

	case "delete":
		slog.Info("knot member deleted", "knot", evt.Did, "member", commit.Rkey)
	}
}

func (s *Subscriber) Cursor() int64 {
	return s.cursor
}

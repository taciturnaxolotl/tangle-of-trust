package db

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Vouch struct {
	VoucherDID string
	VoucheeDID string
	Kind      string
	Reason    string
	CreatedAt string
	Seq       int64
	UpdatedAt time.Time
}

type Follow struct {
	ActorDID  string
	SubjectDID string
	CreatedAt string
	UpdatedAt time.Time
}

type Profile struct {
	DID       string
	Handle    string
	AvatarURL string
	UpdatedAt time.Time
}

type KnotMember struct {
	KnotDID string
	MemberDID string
	Rkey      string
	CreatedAt string
	UpdatedAt time.Time
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	needsInit := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		needsInit = true
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	s := &Store{db: db}

	if needsInit {
		if err := s.init(); err != nil {
			db.Close()
			return nil, fmt.Errorf("init db: %w", err)
		}
	}

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate db: %w", err)
	}

	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) init() error {
	schema := `
	CREATE TABLE IF NOT EXISTS vouches (
		voucher_did TEXT NOT NULL,
		vouchee_did TEXT NOT NULL,
		kind TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		seq INTEGER NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (voucher_did, vouchee_did)
	);

	CREATE INDEX IF NOT EXISTS idx_vouches_kind ON vouches(kind);
	CREATE INDEX IF NOT EXISTS idx_vouches_updated_at ON vouches(updated_at);
	CREATE INDEX IF NOT EXISTS idx_vouches_voucher ON vouches(voucher_did);
	CREATE INDEX IF NOT EXISTS idx_vouches_vouchee ON vouches(vouchee_did);

	CREATE TABLE IF NOT EXISTS cursor (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		seq INTEGER NOT NULL
	);

	INSERT OR IGNORE INTO cursor (id, seq) VALUES (1, 0);
	`
	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS follows (
			actor_did TEXT NOT NULL,
			subject_did TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (actor_did, subject_did)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_follows_actor ON follows(actor_did)`,
		`CREATE INDEX IF NOT EXISTS idx_follows_subject ON follows(subject_did)`,
		`CREATE INDEX IF NOT EXISTS idx_follows_updated_at ON follows(updated_at)`,

		`CREATE TABLE IF NOT EXISTS stars (
			actor_did TEXT NOT NULL,
			subject_did TEXT NOT NULL,
			subject_uri TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (actor_did, subject_uri)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_stars_actor ON stars(actor_did)`,
		`CREATE INDEX IF NOT EXISTS idx_stars_subject_did ON stars(subject_did)`,
		`CREATE INDEX IF NOT EXISTS idx_stars_updated_at ON stars(updated_at)`,

		`CREATE TABLE IF NOT EXISTS knot_members (
			knot_did TEXT NOT NULL,
			member_did TEXT NOT NULL,
			rkey TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (knot_did, member_did)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_knot_members_knot ON knot_members(knot_did)`,
		`CREATE INDEX IF NOT EXISTS idx_knot_members_member ON knot_members(member_did)`,

		`CREATE TABLE IF NOT EXISTS profiles (
			did TEXT PRIMARY KEY,
			handle TEXT NOT NULL DEFAULT '',
			avatar_url TEXT NOT NULL DEFAULT '',
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_profiles_handle ON profiles(handle)`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertVouch(v Vouch) error {
	_, err := s.db.Exec(`
		INSERT INTO vouches (voucher_did, vouchee_did, kind, reason, created_at, seq, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(voucher_did, vouchee_did) DO UPDATE SET
			kind = excluded.kind,
			reason = excluded.reason,
			created_at = excluded.created_at,
			seq = excluded.seq,
			updated_at = excluded.updated_at
	`, v.VoucherDID, v.VoucheeDID, v.Kind, v.Reason, v.CreatedAt, v.Seq, v.UpdatedAt)
	return err
}

func (s *Store) BatchUpsertVouches(vouches []Vouch) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO vouches (voucher_did, vouchee_did, kind, reason, created_at, seq, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(voucher_did, vouchee_did) DO UPDATE SET
			kind = excluded.kind,
			reason = excluded.reason,
			created_at = excluded.created_at,
			seq = excluded.seq,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, v := range vouches {
		if _, err := stmt.Exec(v.VoucherDID, v.VoucheeDID, v.Kind, v.Reason, v.CreatedAt, v.Seq, v.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteVouch(voucherDID, voucheeDID string) error {
	_, err := s.db.Exec(`DELETE FROM vouches WHERE voucher_did = ? AND vouchee_did = ?`, voucherDID, voucheeDID)
	return err
}

func (s *Store) UpsertFollow(f Follow) error {
	_, err := s.db.Exec(`
		INSERT INTO follows (actor_did, subject_did, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(actor_did, subject_did) DO UPDATE SET
			created_at = excluded.created_at,
			updated_at = excluded.updated_at
	`, f.ActorDID, f.SubjectDID, f.CreatedAt, f.UpdatedAt)
	return err
}

func (s *Store) BatchUpsertFollows(follows []Follow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO follows (actor_did, subject_did, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(actor_did, subject_did) DO UPDATE SET
			created_at = excluded.created_at,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, f := range follows {
		if _, err := stmt.Exec(f.ActorDID, f.SubjectDID, f.CreatedAt, f.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteFollow(actorDID, subjectDID string) error {
	_, err := s.db.Exec(`DELETE FROM follows WHERE actor_did = ? AND subject_did = ?`, actorDID, subjectDID)
	return err
}

func (s *Store) UpsertKnotMember(km KnotMember) error {
	_, err := s.db.Exec(`
		INSERT INTO knot_members (knot_did, member_did, rkey, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(knot_did, member_did) DO UPDATE SET
			rkey = excluded.rkey,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at
	`, km.KnotDID, km.MemberDID, km.Rkey, km.CreatedAt, km.UpdatedAt)
	return err
}

func (s *Store) UpsertProfile(p Profile) error {
	_, err := s.db.Exec(`
		INSERT INTO profiles (did, handle, avatar_url, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(did) DO UPDATE SET
			handle = excluded.handle,
			avatar_url = excluded.avatar_url,
			updated_at = excluded.updated_at
	`, p.DID, p.Handle, p.AvatarURL, p.UpdatedAt)
	return err
}

func (s *Store) GetProfile(did string) (Profile, error) {
	var p Profile
	err := s.db.QueryRow(`SELECT did, handle, avatar_url, updated_at FROM profiles WHERE did = ?`, did).
		Scan(&p.DID, &p.Handle, &p.AvatarURL, &p.UpdatedAt)
	return p, err
}

func (s *Store) AllProfiles() (map[string]Profile, error) {
	rows, err := s.db.Query(`SELECT did, handle, avatar_url, updated_at FROM profiles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make(map[string]Profile)
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.DID, &p.Handle, &p.AvatarURL, &p.UpdatedAt); err != nil {
			return nil, err
		}
		profiles[p.DID] = p
	}
	return profiles, rows.Err()
}

func (s *Store) ProfilesNeedingResolution(limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT d.did FROM (
			SELECT voucher_did as did FROM vouches
			UNION SELECT vouchee_did FROM vouches
			UNION SELECT actor_did FROM follows
			UNION SELECT subject_did FROM follows
			UNION SELECT member_did FROM knot_members
		) d
		WHERE d.did LIKE 'did:%'
		AND d.did NOT IN (
			SELECT did FROM profiles WHERE handle != '' AND handle != '!'
		)
		AND d.did NOT IN (
			SELECT did FROM profiles WHERE handle = '!'
		)
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dids []string
	for rows.Next() {
		var did string
		if err := rows.Scan(&did); err != nil {
			return nil, err
		}
		dids = append(dids, did)
	}
	return dids, rows.Err()
}

func (s *Store) DeleteIncompleteProfiles() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM profiles WHERE handle = '' OR avatar_url = ''`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) SaveCursor(seq int64) error {
	_, err := s.db.Exec(`UPDATE cursor SET seq = ? WHERE id = 1`, seq)
	return err
}

func (s *Store) LoadCursor() (int64, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT seq FROM cursor WHERE id = 1`).Scan(&seq)
	return seq, err
}

func (s *Store) VouchesAt(t time.Time) ([]Vouch, error) {
	rows, err := s.db.Query(`
		SELECT voucher_did, vouchee_did, kind, reason, created_at, seq, updated_at
		FROM vouches WHERE updated_at <= ? ORDER BY updated_at ASC
	`, t.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVouches(rows)
}

func (s *Store) AllVouches() ([]Vouch, error) {
	rows, err := s.db.Query(`
		SELECT voucher_did, vouchee_did, kind, reason, created_at, seq, updated_at
		FROM vouches ORDER BY updated_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVouches(rows)
}

func scanVouches(rows *sql.Rows) ([]Vouch, error) {
	var vouches []Vouch
	for rows.Next() {
		var v Vouch
		if err := rows.Scan(&v.VoucherDID, &v.VoucheeDID, &v.Kind, &v.Reason, &v.CreatedAt, &v.Seq, &v.UpdatedAt); err != nil {
			return nil, err
		}
		vouches = append(vouches, v)
	}
	return vouches, rows.Err()
}

func (s *Store) AllFollows() ([]Follow, error) {
	rows, err := s.db.Query(`SELECT actor_did, subject_did, created_at, updated_at FROM follows ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var follows []Follow
	for rows.Next() {
		var f Follow
		if err := rows.Scan(&f.ActorDID, &f.SubjectDID, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		follows = append(follows, f)
	}
	return follows, rows.Err()
}

func (s *Store) AllKnotMembers() ([]KnotMember, error) {
	rows, err := s.db.Query(`SELECT knot_did, member_did, rkey, created_at, updated_at FROM knot_members ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []KnotMember
	for rows.Next() {
		var km KnotMember
		if err := rows.Scan(&km.KnotDID, &km.MemberDID, &km.Rkey, &km.CreatedAt, &km.UpdatedAt); err != nil {
			return nil, err
		}
		members = append(members, km)
	}
	return members, rows.Err()
}

func (s *Store) AllDIDs() ([]string, error) {
	didSet := make(map[string]bool)

	vouches, _ := s.AllVouches()
	for _, v := range vouches {
		didSet[v.VoucherDID] = true
		didSet[v.VoucheeDID] = true
	}

	follows, _ := s.AllFollows()
	for _, f := range follows {
		didSet[f.ActorDID] = true
		didSet[f.SubjectDID] = true
	}

	members, _ := s.AllKnotMembers()
	for _, km := range members {
		didSet[km.KnotDID] = true
		didSet[km.MemberDID] = true
	}

	dids := make([]string, 0, len(didSet))
	for d := range didSet {
		dids = append(dids, d)
	}
	return dids, nil
}

func (s *Store) Stats() (total int64, vouchCount int64, denounceCount int64, followCount int64, knotCount int64, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*) FROM vouches`).Scan(&total)
	if err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM vouches WHERE kind = 'vouch'`).Scan(&vouchCount)
	if err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM vouches WHERE kind = 'denounce'`).Scan(&denounceCount)
	if err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM follows`).Scan(&followCount)
	if err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM knot_members`).Scan(&knotCount)
	return
}

func (s *Store) TimeRange() (min time.Time, max time.Time, err error) {
	err = s.db.QueryRow(`
		SELECT MIN(t), MAX(t) FROM (
			SELECT updated_at as t FROM vouches WHERE updated_at > '0001-01-01'
			UNION ALL SELECT updated_at FROM follows WHERE updated_at > '0001-01-01'
			UNION ALL SELECT updated_at FROM knot_members WHERE updated_at > '0001-01-01'
		)
	`).Scan(&min, &max)
	return
}

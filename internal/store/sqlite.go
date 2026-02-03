package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type AlertRecord struct {
	TS              int64  `json:"ts"`
	Priority        string `json:"priority"`
	GroupName       string `json:"group"`
	Title           string `json:"title"`
	DedupKey        string `json:"dedup_key"`
	MergeKey        string `json:"merge_key"`
	Status          string `json:"status"`
	Channel         string `json:"channel"`
	DingTalkErrCode int    `json:"dingtalk_errcode"`
	DingTalkErrMsg  string `json:"dingtalk_errmsg"`
	PayloadMD       string `json:"payload_md"`
	CreatedAt       string `json:"created_at"`
}

type EventRecord struct {
	ID           int64  `json:"id"`
	TS           int64  `json:"ts"`
	Type         string `json:"type"`
	Severity     string `json:"severity"`
	GroupName    string `json:"group"`
	Title        string `json:"title"`
	DedupKey     string `json:"dedup_key"`
	MergeKey     string `json:"merge_key"`
	EvidenceJSON string `json:"evidence_json"`
	CreatedAt    string `json:"created_at"`
}

type MarketSnapshot struct {
	TS        int64   `json:"ts"`
	Symbol    string  `json:"symbol"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"change_pct"`
	Volume    float64 `json:"volume"`
	Raw       string  `json:"raw"`
	CreatedAt string  `json:"created_at"`
}

type PlanRecord struct {
	Date        string `json:"date"`
	ContentJSON string `json:"content_json"`
	ContentMD   string `json:"content_md"`
	Confirmed   bool   `json:"confirmed"`
	CreatedAt   string `json:"created_at"`
}

func Open(path string) (*Store, error) {
	if path == "" {
		path = "data/app.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma wal: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=3000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma busy_timeout: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS alerts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			priority TEXT,
			group_name TEXT,
			title TEXT,
			dedup_key TEXT,
			merge_key TEXT,
			status TEXT,
			channel TEXT,
			dingtalk_errcode INTEGER,
			dingtalk_errmsg TEXT,
			payload_md TEXT,
			created_at TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_ts ON alerts(ts);`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_status ON alerts(status);`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_group ON alerts(group_name);`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_dedup ON alerts(dedup_key);`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			type TEXT,
			severity TEXT,
			group_name TEXT,
			title TEXT,
			dedup_key TEXT,
			merge_key TEXT,
			evidence_json TEXT,
			created_at TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);`,
		`CREATE INDEX IF NOT EXISTS idx_events_group ON events(group_name);`,
		`CREATE TABLE IF NOT EXISTS market_snapshot (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			symbol TEXT,
			price REAL,
			change_pct REAL,
			volume REAL,
			raw TEXT,
			created_at TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_market_snapshot_ts ON market_snapshot(ts);`,
		`CREATE INDEX IF NOT EXISTS idx_market_snapshot_symbol ON market_snapshot(symbol);`,
		`CREATE TABLE IF NOT EXISTS plan (
			date TEXT PRIMARY KEY,
			content_json TEXT,
			content_md TEXT,
			confirmed INTEGER,
			created_at TEXT
		);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func (s *Store) InsertAlert(a AlertRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if a.CreatedAt == "" {
		a.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, err := s.db.Exec(
		`INSERT INTO alerts (ts, priority, group_name, title, dedup_key, merge_key, status, channel, dingtalk_errcode, dingtalk_errmsg, payload_md, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.TS, a.Priority, a.GroupName, a.Title, a.DedupKey, a.MergeKey, a.Status, a.Channel, a.DingTalkErrCode, a.DingTalkErrMsg, a.PayloadMD, a.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert alert: %w", err)
	}
	return nil
}

func (s *Store) InsertEvent(e EventRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, err := s.db.Exec(
		`INSERT INTO events (ts, type, severity, group_name, title, dedup_key, merge_key, evidence_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS, e.Type, e.Severity, e.GroupName, e.Title, e.DedupKey, e.MergeKey, e.EvidenceJSON, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

func (s *Store) InsertEventReturnID(e EventRecord) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().Format(time.RFC3339)
	}
	res, err := s.db.Exec(
		`INSERT INTO events (ts, type, severity, group_name, title, dedup_key, merge_key, evidence_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS, e.Type, e.Severity, e.GroupName, e.Title, e.DedupKey, e.MergeKey, e.EvidenceJSON, e.CreatedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

func (s *Store) QueryAlertsByDate(date string, status string, group string, limit int, offset int) ([]AlertRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	start, end, err := dateRange(date)
	if err != nil {
		return nil, err
	}

	query := `SELECT ts, priority, group_name, title, dedup_key, merge_key, status, channel, dingtalk_errcode, dingtalk_errmsg, payload_md, created_at
		FROM alerts WHERE ts >= ? AND ts < ?`
	args := []any{start, end}
	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	if group != "" {
		query += " AND group_name = ?"
		args = append(args, group)
	}
	query += " ORDER BY ts DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query alerts: %w", err)
	}
	defer rows.Close()

	var out []AlertRecord
	for rows.Next() {
		var a AlertRecord
		if err := rows.Scan(&a.TS, &a.Priority, &a.GroupName, &a.Title, &a.DedupKey, &a.MergeKey, &a.Status, &a.Channel, &a.DingTalkErrCode, &a.DingTalkErrMsg, &a.PayloadMD, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan alert: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows alert: %w", err)
	}
	return out, nil
}

func (s *Store) QueryAlertsByDedupKey(key string) ([]AlertRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	rows, err := s.db.Query(
		`SELECT ts, priority, group_name, title, dedup_key, merge_key, status, channel, dingtalk_errcode, dingtalk_errmsg, payload_md, created_at
		FROM alerts WHERE dedup_key = ? ORDER BY ts DESC`,
		key,
	)
	if err != nil {
		return nil, fmt.Errorf("query alerts dedup: %w", err)
	}
	defer rows.Close()

	var out []AlertRecord
	for rows.Next() {
		var a AlertRecord
		if err := rows.Scan(&a.TS, &a.Priority, &a.GroupName, &a.Title, &a.DedupKey, &a.MergeKey, &a.Status, &a.Channel, &a.DingTalkErrCode, &a.DingTalkErrMsg, &a.PayloadMD, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan alert: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows alert: %w", err)
	}
	return out, nil
}

func (s *Store) QueryEventsByDate(date string, eventType string, limit int, offset int) ([]EventRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	start, end, err := dateRange(date)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	query := `SELECT id, ts, type, severity, group_name, title, dedup_key, merge_key, evidence_json, created_at
		FROM events WHERE ts >= ? AND ts < ?`
	args := []any{start, end}
	if eventType != "" {
		query += " AND type = ?"
		args = append(args, eventType)
	}
	query += " ORDER BY ts DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		if err := rows.Scan(&e.ID, &e.TS, &e.Type, &e.Severity, &e.GroupName, &e.Title, &e.DedupKey, &e.MergeKey, &e.EvidenceJSON, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows event: %w", err)
	}
	return out, nil
}

func (s *Store) GetEventByID(id int64) (*EventRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	row := s.db.QueryRow(`SELECT id, ts, type, severity, group_name, title, dedup_key, merge_key, evidence_json, created_at FROM events WHERE id = ?`, id)
	var e EventRecord
	if err := row.Scan(&e.ID, &e.TS, &e.Type, &e.Severity, &e.GroupName, &e.Title, &e.DedupKey, &e.MergeKey, &e.EvidenceJSON, &e.CreatedAt); err != nil {
		return nil, fmt.Errorf("get event: %w", err)
	}
	return &e, nil
}

func (s *Store) InsertMarketSnapshot(ms MarketSnapshot) error {
	if s == nil || s.db == nil {
		return nil
	}
	if ms.CreatedAt == "" {
		ms.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, err := s.db.Exec(
		`INSERT INTO market_snapshot (ts, symbol, price, change_pct, volume, raw, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ms.TS, ms.Symbol, ms.Price, ms.ChangePct, ms.Volume, ms.Raw, ms.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert market snapshot: %w", err)
	}
	return nil
}

func (s *Store) QueryMarketSnapshots(symbol string, limit int, offset int) ([]MarketSnapshot, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	query := `SELECT ts, symbol, price, change_pct, volume, raw, created_at
		FROM market_snapshot WHERE symbol = ?
		ORDER BY ts DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(query, symbol, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query market snapshot: %w", err)
	}
	defer rows.Close()
	var out []MarketSnapshot
	for rows.Next() {
		var ms MarketSnapshot
		if err := rows.Scan(&ms.TS, &ms.Symbol, &ms.Price, &ms.ChangePct, &ms.Volume, &ms.Raw, &ms.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan market snapshot: %w", err)
		}
		out = append(out, ms)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows market snapshot: %w", err)
	}
	return out, nil
}

func (s *Store) UpsertPlan(rec PlanRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = time.Now().Format(time.RFC3339)
	}
	confirmed := 0
	if rec.Confirmed {
		confirmed = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO plan (date, content_json, content_md, confirmed, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(date) DO UPDATE SET content_json=excluded.content_json, content_md=excluded.content_md, confirmed=excluded.confirmed, created_at=excluded.created_at`,
		rec.Date, rec.ContentJSON, rec.ContentMD, confirmed, rec.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert plan: %w", err)
	}
	return nil
}

func (s *Store) GetPlan(date string) (*PlanRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	row := s.db.QueryRow(`SELECT date, content_json, content_md, confirmed, created_at FROM plan WHERE date = ?`, date)
	var rec PlanRecord
	var confirmed int
	if err := row.Scan(&rec.Date, &rec.ContentJSON, &rec.ContentMD, &confirmed, &rec.CreatedAt); err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}
	rec.Confirmed = confirmed == 1
	return &rec, nil
}

func (s *Store) ConfirmPlan(date string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`UPDATE plan SET confirmed = 1 WHERE date = ?`, date)
	if err != nil {
		return fmt.Errorf("confirm plan: %w", err)
	}
	return nil
}

func dateRange(date string) (int64, int64, error) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return 0, 0, fmt.Errorf("load tz: %w", err)
	}
	t, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid date: %q", date)
	}
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	end := start.Add(24 * time.Hour)
	return start.Unix(), end.Unix(), nil
}

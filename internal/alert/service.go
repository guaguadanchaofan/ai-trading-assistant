package alert

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-trading-assistant/internal/push/dingtalk"
	"ai-trading-assistant/internal/store"
)

type Priority string

const (
	PriorityHigh Priority = "high"
	PriorityMed  Priority = "med"
	PriorityLow  Priority = "low"
)

type AlertRequest struct {
	Priority Priority `json:"priority"`
	Group    string   `json:"group"`
	Title    string   `json:"title"`
	Markdown string   `json:"markdown"`
	DedupKey string   `json:"dedup_key"`
	MergeKey string   `json:"merge_key"`
	Silent   bool     `json:"silent"`
}

type Status string

const (
	StatusSent          Status = "sent"
	StatusSuppressed    Status = "suppressed"
	StatusQueuedDigest  Status = "queued_digest"
	StatusMergedPending Status = "merged_pending"
)

type Result struct {
	Status          Status
	Error           error
	DingTalkErrCode int
	DingTalkErrMsg  string
}

type Config struct {
	RateLimit         RateLimitConfig
	DedupWindow       time.Duration
	MergeWindow       time.Duration
	LowDigestInterval time.Duration
}

type RateLimitConfig struct {
	PerMinute int
	Burst     int
}

type Service struct {
	dt      *dingtalk.Client
	cfg     Config
	limiter *TokenBucket
	store   *store.Store

	dedupMu sync.Mutex
	dedup   map[string]time.Time

	mergeMu sync.Mutex
	merge   map[string]*mergeState

	digestMu sync.Mutex
	digest   map[string][]AlertRequest

	stopCh chan struct{}
}

type mergeState struct {
	alerts []AlertRequest
	timer  *time.Timer
}

func NewService(dt *dingtalk.Client, st *store.Store, cfg Config) *Service {
	s := &Service{
		dt:      dt,
		cfg:     cfg,
		limiter: NewTokenBucket(cfg.RateLimit.PerMinute, cfg.RateLimit.Burst),
		store:   st,
		dedup:   make(map[string]time.Time),
		merge:   make(map[string]*mergeState),
		digest:  make(map[string][]AlertRequest),
		stopCh:  make(chan struct{}),
	}
	if cfg.LowDigestInterval > 0 {
		go s.runDigestLoop()
	}
	return s
}

func (s *Service) Handle(ctx context.Context, req AlertRequest) Result {
	req = normalize(req)
	if req.Silent {
		res := Result{Status: StatusSuppressed}
		s.recordAlert(req, res, "")
		return res
	}

	if s.isDeduped(req) {
		res := Result{Status: StatusSuppressed}
		s.recordAlert(req, res, "")
		return res
	}

	if req.MergeKey != "" && s.cfg.MergeWindow > 0 {
		s.enqueueMerge(req)
		res := Result{Status: StatusMergedPending}
		s.recordAlert(req, res, "")
		return res
	}

	res, payload := s.handleSendOrDigest(ctx, req)
	s.recordAlert(req, res, payload)
	return res
}

func (s *Service) handleSendOrDigest(ctx context.Context, req AlertRequest) (Result, string) {
	if req.Priority == PriorityLow {
		s.addDigest(req)
		return Result{Status: StatusQueuedDigest}, ""
	}

	if s.limiter == nil || s.limiter.Allow() {
		return s.sendNow(ctx, req), req.Markdown
	}

	if req.Priority == PriorityHigh {
		if s.limiter.WaitForToken(2 * time.Second) {
			return s.sendNow(ctx, req), req.Markdown
		}
		s.addDigest(req)
		return Result{Status: StatusQueuedDigest}, ""
	}

	// med or others fall back to digest
	s.addDigest(req)
	return Result{Status: StatusQueuedDigest}, ""
}

func (s *Service) sendNow(ctx context.Context, req AlertRequest) Result {
	if s.dt == nil {
		return Result{Status: StatusSent, Error: fmt.Errorf("dingtalk client not configured")}
	}
	resp, err := s.dt.SendMarkdown(ctx, req.Title, req.Markdown)
	if err != nil {
		return Result{Status: StatusSent, Error: err}
	}
	if resp.ErrCode != 0 {
		return Result{
			Status:          StatusSent,
			DingTalkErrCode: resp.ErrCode,
			DingTalkErrMsg:  resp.ErrMsg,
			Error:           fmt.Errorf("dingtalk errcode=%d errmsg=%s", resp.ErrCode, resp.ErrMsg),
		}
	}
	return Result{Status: StatusSent, DingTalkErrCode: resp.ErrCode, DingTalkErrMsg: resp.ErrMsg}
}

func (s *Service) isDeduped(req AlertRequest) bool {
	if req.DedupKey == "" || s.cfg.DedupWindow <= 0 {
		return false
	}
	now := time.Now()
	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()
	if last, ok := s.dedup[req.DedupKey]; ok {
		if now.Sub(last) <= s.cfg.DedupWindow {
			return true
		}
	}
	s.dedup[req.DedupKey] = now
	return false
}

func (s *Service) enqueueMerge(req AlertRequest) {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	state, ok := s.merge[req.MergeKey]
	if !ok {
		state = &mergeState{}
		s.merge[req.MergeKey] = state
		state.timer = time.AfterFunc(s.cfg.MergeWindow, func() {
			s.flushMerge(req.MergeKey)
		})
	}
	state.alerts = append(state.alerts, req)
}

func (s *Service) flushMerge(key string) {
	s.mergeMu.Lock()
	state, ok := s.merge[key]
	if ok {
		delete(s.merge, key)
	}
	s.mergeMu.Unlock()
	if !ok || len(state.alerts) == 0 {
		return
	}

	merged := buildMerged(state.alerts)
	if merged.Silent {
		return
	}

	_ = s.Handle(context.Background(), merged)
}

func (s *Service) addDigest(req AlertRequest) {
	if s.cfg.LowDigestInterval <= 0 {
		return
	}
	s.digestMu.Lock()
	defer s.digestMu.Unlock()
	group := req.Group
	if group == "" {
		group = "default"
	}
	s.digest[group] = append(s.digest[group], req)
}

func (s *Service) runDigestLoop() {
	ticker := time.NewTicker(s.cfg.LowDigestInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flushDigest()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Service) flushDigest() {
	groups := s.swapDigest()
	if len(groups) == 0 {
		return
	}

	if s.dt == nil {
		log.Printf("digest send skipped: dingtalk client not configured")
		return
	}

	title := "Low Alert Digest"
	markdown := buildDigestMarkdown(groups)
	resp, err := s.dt.SendMarkdown(context.Background(), title, markdown)
	if err != nil {
		log.Printf("digest send error: %v", err)
		return
	}
	if resp.ErrCode != 0 {
		log.Printf("digest dingtalk error: errcode=%d errmsg=%s", resp.ErrCode, resp.ErrMsg)
	}
}

func (s *Service) recordAlert(req AlertRequest, res Result, payload string) {
	if s.store == nil {
		return
	}
	ts := time.Now().Unix()
	rec := store.AlertRecord{
		TS:              ts,
		Priority:        string(req.Priority),
		GroupName:       req.Group,
		Title:           req.Title,
		DedupKey:        req.DedupKey,
		MergeKey:        req.MergeKey,
		Status:          string(res.Status),
		Channel:         "dingtalk",
		DingTalkErrCode: res.DingTalkErrCode,
		DingTalkErrMsg:  res.DingTalkErrMsg,
		PayloadMD:       payload,
	}
	if err := s.store.InsertAlert(rec); err != nil {
		log.Printf("insert alert record error: %v", err)
	}

	evt := store.EventRecord{
		TS:           ts,
		Type:         "alert",
		Severity:     string(req.Priority),
		GroupName:    req.Group,
		Title:        req.Title,
		DedupKey:     req.DedupKey,
		MergeKey:     req.MergeKey,
		EvidenceJSON: "",
	}
	if err := s.store.InsertEvent(evt); err != nil {
		log.Printf("insert event record error: %v", err)
	}
}

func (s *Service) swapDigest() map[string][]AlertRequest {
	s.digestMu.Lock()
	defer s.digestMu.Unlock()
	if len(s.digest) == 0 {
		return nil
	}
	out := s.digest
	s.digest = make(map[string][]AlertRequest)
	return out
}

func buildMerged(alerts []AlertRequest) AlertRequest {
	merged := alerts[0]
	merged.MergeKey = ""
	merged.DedupKey = ""
	merged.Priority = maxPriority(alerts)
	merged.Title = mergedTitle(alerts)
	merged.Markdown = mergedMarkdown(alerts)
	merged.Silent = allSilent(alerts)
	return merged
}

func maxPriority(alerts []AlertRequest) Priority {
	p := PriorityLow
	for _, a := range alerts {
		if rank(a.Priority) > rank(p) {
			p = a.Priority
		}
	}
	return p
}

func rank(p Priority) int {
	switch p {
	case PriorityHigh:
		return 3
	case PriorityMed:
		return 2
	case PriorityLow:
		return 1
	default:
		return 2
	}
}

func mergedTitle(alerts []AlertRequest) string {
	if len(alerts) == 1 {
		return alerts[0].Title
	}
	base := alerts[0].Title
	if base == "" {
		base = "Merged Alerts"
	}
	return fmt.Sprintf("%s (+%d)", base, len(alerts)-1)
}

func mergedMarkdown(alerts []AlertRequest) string {
	var b strings.Builder
	for _, a := range alerts {
		title := a.Title
		if title == "" {
			title = "(no title)"
		}
		b.WriteString("- **")
		b.WriteString(title)
		b.WriteString("**")
		if a.Markdown != "" {
			b.WriteString("\n  ")
			b.WriteString(a.Markdown)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func buildDigestMarkdown(groups map[string][]AlertRequest) string {
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, g := range keys {
		b.WriteString("### ")
		b.WriteString(g)
		b.WriteString("\n")
		for _, a := range groups[g] {
			title := a.Title
			if title == "" {
				title = "(no title)"
			}
			b.WriteString("- **")
			b.WriteString(title)
			b.WriteString("**")
			if a.Markdown != "" {
				b.WriteString("\n  ")
				b.WriteString(a.Markdown)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func allSilent(alerts []AlertRequest) bool {
	for _, a := range alerts {
		if !a.Silent {
			return false
		}
	}
	return true
}

func normalize(req AlertRequest) AlertRequest {
	if req.Priority == "" {
		req.Priority = PriorityMed
	}
	if req.Group == "" {
		req.Group = "default"
	}
	return req
}

// TokenBucket is a simple global rate limiter.
type TokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	ratePerS   float64
	burst      float64
	lastRefill time.Time
	disabled   bool
}

func NewTokenBucket(perMinute, burst int) *TokenBucket {
	if perMinute <= 0 {
		return &TokenBucket{disabled: true}
	}
	if burst <= 0 {
		burst = perMinute
	}
	return &TokenBucket{
		tokens:     float64(burst),
		ratePerS:   float64(perMinute) / 60.0,
		burst:      float64(burst),
		lastRefill: time.Now(),
	}
}

func (t *TokenBucket) Allow() bool {
	if t == nil || t.disabled {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refillLocked()
	if t.tokens >= 1 {
		t.tokens -= 1
		return true
	}
	return false
}

func (t *TokenBucket) WaitForToken(maxWait time.Duration) bool {
	if t == nil || t.disabled {
		return true
	}
	deadline := time.Now().Add(maxWait)
	for {
		if t.Allow() {
			return true
		}
		now := time.Now()
		if now.After(deadline) {
			return false
		}
		sleepFor := t.timeUntilNext()
		remaining := deadline.Sub(now)
		if sleepFor > remaining {
			sleepFor = remaining
		}
		if sleepFor > 0 {
			time.Sleep(sleepFor)
		}
	}
}

func (t *TokenBucket) timeUntilNext() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refillLocked()
	if t.tokens >= 1 || t.ratePerS <= 0 {
		return 0
	}
	need := 1 - t.tokens
	sec := need / t.ratePerS
	return time.Duration(sec * float64(time.Second))
}

func (t *TokenBucket) refillLocked() {
	now := time.Now()
	elapsed := now.Sub(t.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	t.tokens += elapsed * t.ratePerS
	if t.tokens > t.burst {
		t.tokens = t.burst
	}
	t.lastRefill = now
}

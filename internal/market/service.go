package market

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"ai-trading-assistant/internal/engine"
	"ai-trading-assistant/internal/store"
)

type Service struct {
	provider    MarketProvider
	minInterval time.Duration
	store       *store.Store
	engine      *engine.Engine

	mu                  sync.Mutex
	lastFetch           time.Time
	cache               map[string]Quote
	consecutiveFailures int
}

func NewService(provider MarketProvider, minInterval time.Duration, st *store.Store, eng *engine.Engine) *Service {
	if minInterval < 0 {
		minInterval = 0
	}
	return &Service{
		provider:    provider,
		minInterval: minInterval,
		store:       st,
		engine:      eng,
		cache:       make(map[string]Quote),
	}
}

func (s *Service) GetQuotes(symbols []string) ([]Quote, error) {
	quotes, _, _, _, _, err := s.GetQuotesWithMeta(symbols)
	return quotes, err
}

func (s *Service) GetQuotesWithMeta(symbols []string) ([]Quote, bool, string, int64, []string, error) {
	if s.provider == nil {
		return nil, false, "", 0, nil, fmt.Errorf("market provider not configured")
	}
	if len(symbols) == 0 {
		return nil, false, "", 0, nil, fmt.Errorf("symbols is empty")
	}

	now := time.Now()
	s.mu.Lock()
	if s.minInterval > 0 && now.Sub(s.lastFetch) < s.minInterval {
		cached, err := s.getFromCacheLocked(symbols)
		sourceTS := maxQuoteTSLocked(cached)
		s.mu.Unlock()
		if err != nil {
			return nil, false, "", 0, nil, err
		}
		return cached, true, "cache", sourceTS, []string{"请求过快，返回缓存数据"}, nil
	}
	s.mu.Unlock()

	quotes, source, err := s.provider.GetQuotes(context.Background(), symbols)
	if err == nil {
		s.mu.Lock()
		for _, q := range quotes {
			s.cache[strings.ToLower(q.Symbol)] = q
		}
		s.lastFetch = time.Now()
		s.consecutiveFailures = 0
		s.mu.Unlock()
		return quotes, false, source, time.Now().Unix(), nil, nil
	}

	s.mu.Lock()
	s.consecutiveFailures++
	cached, cacheErr := s.getFromCacheLocked(symbols)
	sourceTS := maxQuoteTSLocked(cached)
	s.mu.Unlock()
	if cacheErr == nil {
		return cached, true, "cache", sourceTS, []string{fmt.Sprintf("行情获取失败，已返回缓存：%v", err)}, nil
	}

	return nil, false, source, 0, nil, err
}

func (s *Service) PollAndStore(symbols []string) error {
	quotes, _, _, _, _, err := s.GetQuotesWithMeta(symbols)
	if err != nil {
		log.Printf("market poll error: %v", err)
		return err
	}
	for _, q := range quotes {
		snapshot := store.MarketSnapshot{
			TS:        q.TS,
			Symbol:    q.Symbol,
			Name:      q.Name,
			Price:     q.Price,
			ChangePct: q.ChangePct,
			Volume:    q.Volume,
			Raw:       q.Raw,
		}
		s.ingestSnapshot(snapshot)
	}
	return nil
}

func (s *Service) PollLoop(symbols []string, baseInterval time.Duration) {
	if baseInterval <= 0 {
		baseInterval = 3 * time.Second
	}
	for {
		err := s.PollAndStore(symbols)
		interval := s.nextPollInterval(baseInterval, err != nil)
		time.Sleep(interval)
	}
}

func (s *Service) nextPollInterval(base time.Duration, failed bool) time.Duration {
	if !failed {
		return base
	}
	s.mu.Lock()
	failures := s.consecutiveFailures
	s.mu.Unlock()
	if failures >= 6 {
		return base * 4
	}
	if failures >= 3 {
		return base * 2
	}
	return base
}

func (s *Service) getFromCacheLocked(symbols []string) ([]Quote, error) {
	out := make([]Quote, 0, len(symbols))
	for _, sym := range symbols {
		key := strings.ToLower(sym)
		q, ok := s.cache[key]
		if !ok {
			return nil, fmt.Errorf("cache miss for symbol: %s", sym)
		}
		out = append(out, q)
	}
	return out, nil
}

func (s *Service) IngestSnapshot(snapshot store.MarketSnapshot) {
	s.ingestSnapshot(snapshot)
}

func (s *Service) ingestSnapshot(snapshot store.MarketSnapshot) {
	if snapshot.Symbol == "" {
		return
	}
	if snapshot.TS == 0 {
		snapshot.TS = time.Now().Unix()
	}
	if s.store != nil {
		if err := s.store.InsertMarketSnapshot(snapshot); err != nil {
			log.Printf("insert market snapshot error: %v", err)
		}
	}
	if s.engine != nil {
		s.engine.OnSnapshot(snapshot)
	}

	if snapshot.Price > 0 {
		s.mu.Lock()
		s.cache[strings.ToLower(snapshot.Symbol)] = Quote{
			Symbol:    snapshot.Symbol,
			Price:     snapshot.Price,
			ChangePct: snapshot.ChangePct,
			Volume:    snapshot.Volume,
			TS:        snapshot.TS,
			Raw:       snapshot.Raw,
		}
		s.mu.Unlock()
	}
}

func maxQuoteTSLocked(quotes []Quote) int64 {
	var maxTS int64
	for _, q := range quotes {
		if q.TS > maxTS {
			maxTS = q.TS
		}
	}
	return maxTS
}

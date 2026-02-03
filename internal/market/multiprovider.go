package market

import (
	"context"
	"fmt"
)

type MultiProvider struct {
	providers []MarketProvider
}

func NewMultiProvider(providers ...MarketProvider) *MultiProvider {
	return &MultiProvider{providers: providers}
}

func (m *MultiProvider) GetQuotes(ctx context.Context, symbols []string) ([]Quote, string, error) {
	if len(m.providers) == 0 {
		return nil, "", fmt.Errorf("no market providers configured")
	}
	var lastErr error
	for _, p := range m.providers {
		quotes, source, err := p.GetQuotes(ctx, symbols)
		if err == nil && len(quotes) > 0 {
			return quotes, source, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all providers failed")
	}
	return nil, "", lastErr
}

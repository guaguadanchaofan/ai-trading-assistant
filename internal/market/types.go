package market

import "context"

type Quote struct {
	Symbol    string  `json:"symbol"`
	Name      string  `json:"name,omitempty"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"change_pct,omitempty"`
	Volume    float64 `json:"volume,omitempty"`
	TS        int64   `json:"ts"`
	Raw       string  `json:"raw,omitempty"`
}

type MarketProvider interface {
	GetQuotes(ctx context.Context, symbols []string) ([]Quote, string, error)
}

package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type EastmoneyProvider struct {
	baseURL string
	client  *http.Client
}

type eastmoneyResp struct {
	Data *eastmoneyData `json:"data"`
}

type eastmoneyData struct {
	Name      string  `json:"f58"`
	Code      string  `json:"f57"`
	Price     float64 `json:"f43"`
	ChangePct float64 `json:"f170"`
	Volume    float64 `json:"f47"`
}

func NewEastmoneyProvider(timeout time.Duration) *EastmoneyProvider {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &EastmoneyProvider{
		baseURL: "https://push2.eastmoney.com/api/qt/stock/get",
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *EastmoneyProvider) GetQuotes(ctx context.Context, symbols []string) ([]Quote, string, error) {
	if len(symbols) == 0 {
		return nil, "", fmt.Errorf("symbols is empty")
	}
	out := make([]Quote, 0, len(symbols))
	for _, sym := range symbols {
		q, err := p.getOne(ctx, sym)
		if err != nil {
			return nil, "", err
		}
		out = append(out, q)
	}
	return out, "eastmoney", nil
}

func (p *EastmoneyProvider) getOne(ctx context.Context, symbol string) (Quote, error) {
	secid, err := toSecID(symbol)
	if err != nil {
		return Quote{}, err
	}

	u, err := url.Parse(p.baseURL)
	if err != nil {
		return Quote{}, fmt.Errorf("invalid base url: %w", err)
	}
	q := u.Query()
	q.Set("secid", secid)
	q.Set("fields", "f57,f58,f43,f170,f47")
	q.Set("ut", "fa5fd1943c7b386f172d6893dbfba10b")
	q.Set("fltt", "2")
	q.Set("invt", "2")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Quote{}, fmt.Errorf("build request: %w", err)
	}

	var payload eastmoneyResp
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := p.client.Do(req)
		if err != nil {
			if shouldRetry(err) && attempt < 2 {
				lastErr = err
				time.Sleep(150 * time.Millisecond)
				continue
			}
			return Quote{}, fmt.Errorf("request eastmoney: %w", err)
		}
		defer resp.Body.Close()

		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			if shouldRetry(err) && attempt < 2 {
				lastErr = err
				time.Sleep(150 * time.Millisecond)
				continue
			}
			return Quote{}, fmt.Errorf("decode eastmoney: %w", err)
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return Quote{}, fmt.Errorf("request eastmoney: %w", lastErr)
	}
	if payload.Data == nil {
		return Quote{}, fmt.Errorf("empty response data")
	}
	if payload.Data.Price <= 0 {
		return Quote{}, fmt.Errorf("invalid price for %s", symbol)
	}

	rawBytes, _ := json.Marshal(payload.Data)
	return Quote{
		Symbol:    strings.ToLower(symbol),
		Name:      payload.Data.Name,
		Price:     payload.Data.Price,
		ChangePct: payload.Data.ChangePct,
		Volume:    payload.Data.Volume,
		TS:        time.Now().Unix(),
		Raw:       string(rawBytes),
	}, nil
}

func toSecID(symbol string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(symbol))
	if strings.HasPrefix(s, "sh") {
		return "1." + strings.TrimPrefix(s, "sh"), nil
	}
	if strings.HasPrefix(s, "sz") {
		return "0." + strings.TrimPrefix(s, "sz"), nil
	}
	return "", fmt.Errorf("invalid symbol: %s", symbol)
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection reset") || strings.Contains(msg, "reset by peer") {
		return true
	}
	return false
}

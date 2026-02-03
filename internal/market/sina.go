package market

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type SinaProvider struct {
	baseURL string
	client  *http.Client
}

func NewSinaProvider(timeout time.Duration) *SinaProvider {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &SinaProvider{
		baseURL: "https://hq.sinajs.cn/list=",
		client:  &http.Client{Timeout: timeout},
	}
}

func (p *SinaProvider) GetQuotes(ctx context.Context, symbols []string) ([]Quote, string, error) {
	if len(symbols) == 0 {
		return nil, "", fmt.Errorf("symbols is empty")
	}
	list := strings.Join(symbols, ",")
	url := p.baseURL + list
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request sina: %w", err)
	}
	defer resp.Body.Close()
	data, err := readAll(resp)
	if err != nil {
		return nil, "", fmt.Errorf("read sina: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	out := make([]Quote, 0, len(symbols))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		q, ok := parseSinaLine(line)
		if ok {
			out = append(out, q)
		}
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("empty sina response")
	}
	return out, "sina", nil
}

func parseSinaLine(line string) (Quote, bool) {
	// format: var hq_str_sh000001="name,open,preclose,price,high,low,....,volume,amount,...";
	parts := strings.Split(line, "=")
	if len(parts) < 2 {
		return Quote{}, false
	}
	sym := strings.TrimPrefix(strings.TrimSpace(parts[0]), "var hq_str_")
	payload := strings.Trim(parts[1], ";")
	payload = strings.Trim(payload, "\"")
	fields := strings.Split(payload, ",")
	if len(fields) < 10 {
		return Quote{}, false
	}
	name := fields[0]
	price := parseFloat(fields[3])
	preclose := parseFloat(fields[2])
	volume := parseFloat(fields[8])
	if price <= 0 {
		return Quote{}, false
	}
	changePct := 0.0
	if preclose > 0 {
		changePct = (price - preclose) / preclose * 100
	}
	return Quote{
		Symbol:    strings.ToLower(sym),
		Name:      name,
		Price:     price,
		ChangePct: changePct,
		Volume:    volume,
		TS:        time.Now().Unix(),
		Raw:       payload,
	}, true
}

func readAll(resp *http.Response) ([]byte, error) {
	return io.ReadAll(resp.Body)
}

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-trading-assistant/internal/alert"
	"ai-trading-assistant/internal/engine"
	"ai-trading-assistant/internal/market"
	"ai-trading-assistant/internal/planagent"
	"ai-trading-assistant/internal/push/dingtalk"
	"ai-trading-assistant/internal/riskagent"
	"ai-trading-assistant/internal/store"

	"database/sql"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

type TestPushRequest struct {
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

type AlertResponse struct {
	OK              bool   `json:"ok"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	DingTalkErrCode int    `json:"dingtalk_errcode,omitempty"`
	DingTalkErrMsg  string `json:"dingtalk_errmsg,omitempty"`
}

func RegisterRoutes(h *server.Hertz, dt *dingtalk.Client, alertSvc *alert.Service, st *store.Store, mkt *market.Service, defaultSymbols []string, eng *engine.Engine, agent *riskagent.Agent, planAgent *planagent.Agent) {
	h.GET("/healthz", func(_ context.Context, c *app.RequestContext) {
		c.JSON(200, map[string]bool{"ok": true})
	})

	h.POST("/api/v1/test/push", func(_ context.Context, c *app.RequestContext) {
		if dt == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "dingtalk client not configured",
			})
			return
		}

		var req TestPushRequest
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "invalid json body",
			})
			return
		}

		resp, err := dt.SendMarkdown(context.Background(), req.Title, req.Markdown)
		if err != nil {
			log.Printf("dingtalk send error: %v", err)
			c.JSON(http.StatusBadGateway, map[string]any{
				"ok":               false,
				"error":            err.Error(),
				"dingtalk_errcode": 0,
				"dingtalk_errmsg":  "",
			})
			return
		}

		if resp.ErrCode != 0 {
			c.JSON(http.StatusBadGateway, map[string]any{
				"ok":               false,
				"error":            "dingtalk returned error",
				"dingtalk_errcode": resp.ErrCode,
				"dingtalk_errmsg":  resp.ErrMsg,
			})
			return
		}

		c.JSON(http.StatusOK, map[string]any{
			"ok":               true,
			"dingtalk_errcode": resp.ErrCode,
			"dingtalk_errmsg":  resp.ErrMsg,
		})
	})

	h.POST("/api/v1/test/alert", func(_ context.Context, c *app.RequestContext) {
		if alertSvc == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "alert service not configured",
			})
			return
		}

		var req alert.AlertRequest
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "invalid json body",
			})
			return
		}

		res := alertSvc.Handle(context.Background(), req)
		resp := AlertResponse{
			OK:              res.Error == nil,
			Status:          string(res.Status),
			DingTalkErrCode: res.DingTalkErrCode,
			DingTalkErrMsg:  res.DingTalkErrMsg,
		}
		if res.Error != nil {
			resp.Error = res.Error.Error()
		}
		c.JSON(http.StatusOK, resp)
	})

	h.POST("/api/v1/test/alert-burst", func(_ context.Context, c *app.RequestContext) {
		if alertSvc == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "alert service not configured",
			})
			return
		}

		stats := map[string]int{
			"sent":           0,
			"suppressed":     0,
			"queued_digest":  0,
			"merged_pending": 0,
			"error":          0,
		}

		now := time.Now().Unix()
		for i := 0; i < 50; i++ {
			req := alert.AlertRequest{
				Priority: pickPriority(i),
				Group:    pickGroup(i),
				Title:    "Test Alert",
				Markdown: "burst test message",
				DedupKey: pickDedupKey(i),
				MergeKey: pickMergeKey(i),
				Silent:   false,
			}
			req.Title = req.Title + " #" + fmtInt(i)
			req.Markdown = req.Markdown + " (" + fmtInt(int(now)) + ")"
			res := alertSvc.Handle(context.Background(), req)
			if res.Error != nil {
				stats["error"]++
			}
			switch res.Status {
			case alert.StatusSent:
				stats["sent"]++
			case alert.StatusSuppressed:
				stats["suppressed"]++
			case alert.StatusQueuedDigest:
				stats["queued_digest"]++
			case alert.StatusMergedPending:
				stats["merged_pending"]++
			}
		}

		c.JSON(http.StatusOK, map[string]any{
			"ok":    true,
			"stats": stats,
		})
	})

	h.GET("/api/v1/alerts", func(_ context.Context, c *app.RequestContext) {
		if st == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "store not configured",
			})
			return
		}

		date := string(c.Query("date"))
		status := string(c.Query("status"))
		group := string(c.Query("group"))
		limit, err := parseLimit(c.Query("limit"))
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		offset, err := parseOffset(c.Query("offset"))
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		if date == "" {
			date = chinaToday()
		}

		items, err := st.QueryAlertsByDate(date, status, group, limit, offset)
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, map[string]any{
			"ok":    true,
			"items": items,
		})
	})

	h.GET("/api/v1/alerts/dedup/:key", func(_ context.Context, c *app.RequestContext) {
		if st == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "store not configured",
			})
			return
		}
		key := c.Param("key")
		if key == "" {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "dedup_key is required",
			})
			return
		}
		items, err := st.QueryAlertsByDedupKey(key)
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, map[string]any{
			"ok":    true,
			"items": items,
		})
	})

	h.GET("/api/v1/quotes", func(_ context.Context, c *app.RequestContext) {
		if mkt == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "market service not configured",
			})
			return
		}
		symbols := parseSymbols(string(c.Query("symbols")), defaultSymbols)
		if len(symbols) == 0 {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "symbols is empty",
			})
			return
		}
		quotes, stale, source, sourceTS, warnings, err := mkt.GetQuotesWithMeta(symbols)
		if err != nil && len(quotes) == 0 {
			c.JSON(http.StatusBadGateway, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("quotes fetch failed: %v", err))
		}
		c.JSON(http.StatusOK, map[string]any{
			"ok":        true,
			"stale":     stale,
			"source":    source,
			"source_ts": sourceTS,
			"warnings":  warnings,
			"quotes":    quotes,
		})
	})

	h.GET("/api/v1/snapshots", func(_ context.Context, c *app.RequestContext) {
		if st == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "store not configured",
			})
			return
		}
		symbol := string(c.Query("symbol"))
		if symbol == "" {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "symbol is required",
			})
			return
		}
		limit, err := parseLimit(c.Query("limit"))
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		offset, err := parseOffset(c.Query("offset"))
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		items, err := st.QueryMarketSnapshots(symbol, limit, offset)
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, map[string]any{
			"ok":    true,
			"items": items,
		})
	})

	h.POST("/api/v1/test/snapshot", func(_ context.Context, c *app.RequestContext) {
		if st == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "store not configured",
			})
			return
		}
		var req struct {
			Symbol    string  `json:"symbol"`
			Price     float64 `json:"price"`
			ChangePct float64 `json:"change_pct"`
			Volume    float64 `json:"volume"`
			TS        int64   `json:"ts"`
		}
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "invalid json body",
			})
			return
		}
		if req.Symbol == "" || req.Price <= 0 {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "symbol and price are required",
			})
			return
		}
		if req.TS == 0 {
			req.TS = time.Now().Unix()
		}
		snapshot := store.MarketSnapshot{
			TS:        req.TS,
			Symbol:    req.Symbol,
			Price:     req.Price,
			ChangePct: req.ChangePct,
			Volume:    req.Volume,
		}
		if mkt != nil {
			mkt.IngestSnapshot(snapshot)
		} else {
			if err := st.InsertMarketSnapshot(snapshot); err != nil {
				c.JSON(http.StatusBadRequest, map[string]any{
					"ok":    false,
					"error": err.Error(),
				})
				return
			}
			if eng != nil {
				eng.OnSnapshot(snapshot)
			}
		}
		c.JSON(http.StatusOK, map[string]any{
			"ok": true,
		})
	})

	h.GET("/api/v1/events", func(_ context.Context, c *app.RequestContext) {
		if st == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "store not configured",
			})
			return
		}
		date := string(c.Query("date"))
		if date == "" {
			date = chinaToday()
		}
		eventType := string(c.Query("type"))
		limit, err := parseLimit(c.Query("limit"))
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		offset, err := parseOffset(c.Query("offset"))
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		items, err := st.QueryEventsByDate(date, eventType, limit, offset)
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, map[string]any{
			"ok":    true,
			"items": items,
		})
	})

	h.POST("/api/v1/test/risk/eval", func(_ context.Context, c *app.RequestContext) {
		if st == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "store not configured",
			})
			return
		}
		var req struct {
			EventID int64              `json:"event_id"`
			Event   *store.EventRecord `json:"event"`
		}
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "invalid json body",
			})
			return
		}
		var evt *store.EventRecord
		if req.EventID > 0 {
			e, err := st.GetEventByID(req.EventID)
			if err != nil {
				c.JSON(http.StatusBadRequest, map[string]any{
					"ok":    false,
					"error": err.Error(),
				})
				return
			}
			evt = e
		} else if req.Event != nil {
			evt = req.Event
		} else {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "event_id or event is required",
			})
			return
		}
		input := riskagent.EventInput{
			EventID:  evt.ID,
			Type:     evt.Type,
			Severity: evt.Severity,
			Symbol:   extractSymbolFromTitle(evt.Title),
			Evidence: evt.EvidenceJSON,
		}
		applyEvidenceFields(&input, evt.EvidenceJSON)
		decision := riskagent.FallbackDecision(input)
		if agent != nil {
			if d, err := agent.Evaluate(context.Background(), input); err == nil {
				decision = d
			} else {
				log.Printf("risk eval error: %v", err)
			}
		}
		markdown := riskagent.FormatMarkdown(evt.Title, decision)
		c.JSON(http.StatusOK, map[string]any{
			"ok":       true,
			"decision": decision,
			"markdown": markdown,
		})
	})

	h.POST("/api/v1/test/risk/ping", func(_ context.Context, c *app.RequestContext) {
		if agent == nil {
			c.JSON(http.StatusOK, map[string]any{
				"ok":     true,
				"mode":   "fallback",
				"reason": "risk agent not configured",
			})
			return
		}
		resp, err := agent.Ping(context.Background())
		if err != nil {
			c.JSON(http.StatusOK, resp)
			return
		}
		c.JSON(http.StatusOK, resp)
	})

	h.POST("/api/v1/plan/generate", func(_ context.Context, c *app.RequestContext) {
		if st == nil || mkt == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "store or market not configured",
			})
			return
		}
		date := string(c.Query("date"))
		if date == "" {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "date is required (YYYY-MM-DD)",
			})
			return
		}
		if _, err := time.Parse("2006-01-02", date); err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "invalid date format (YYYY-MM-DD)",
			})
			return
		}

		symbols := ensureIndexSymbol(parseSymbols(string(c.Query("symbols")), defaultSymbols))
		var warnings []string
		quotes, stale, source, sourceTS, w, qErr := mkt.GetQuotesWithMeta(symbols)
		warnings = append(warnings, w...)
		if qErr != nil && len(quotes) == 0 {
			warnings = append(warnings, fmt.Sprintf("quotes fetch failed: %v", qErr))
		} else if stale {
			warnings = append(warnings, fmt.Sprintf("quotes stale, source=%s source_ts=%d", source, sourceTS))
		}

		input := planagent.Input{Date: date, Quotes: quotes}
		plan := planagent.FallbackPlan(input)
		mode := "fallback"
		if planAgent != nil && qErr == nil {
			if p, err := planAgent.Evaluate(context.Background(), input); err == nil {
				plan = p
				mode = "llm"
			} else {
				log.Printf("planagent eval error: %v", err)
				warnings = append(warnings, "planagent eval failed, fallback used")
			}
		}
		contentJSON, _ := json.Marshal(plan)
		if err := st.UpsertPlan(store.PlanRecord{
			Date:        date,
			ContentJSON: string(contentJSON),
			Confirmed:   false,
		}); err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, map[string]any{
			"ok":       true,
			"mode":     mode,
			"plan":     plan,
			"warnings": warnings,
		})
	})

	h.POST("/api/v1/plan/confirm", func(_ context.Context, c *app.RequestContext) {
		if st == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "store not configured",
			})
			return
		}
		var req struct {
			Date string `json:"date"`
		}
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "invalid json body",
			})
			return
		}
		if req.Date == "" {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "date is required (YYYY-MM-DD)",
			})
			return
		}
		if _, err := st.GetPlan(req.Date); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				c.JSON(http.StatusBadRequest, map[string]any{
					"ok":    false,
					"error": "plan not found",
				})
				return
			}
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		if err := st.ConfirmPlan(req.Date); err != nil {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, map[string]any{"ok": true})
	})

	h.GET("/api/v1/plan", func(_ context.Context, c *app.RequestContext) {
		if st == nil {
			c.JSON(http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "store not configured",
			})
			return
		}
		date := string(c.Query("date"))
		if date == "" {
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "date is required (YYYY-MM-DD)",
			})
			return
		}
		rec, err := st.GetPlan(date)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				c.JSON(http.StatusNotFound, map[string]any{
					"ok":    false,
					"error": "plan not found",
				})
				return
			}
			c.JSON(http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		var plan planagent.Plan
		_ = json.Unmarshal([]byte(rec.ContentJSON), &plan)
		c.JSON(http.StatusOK, map[string]any{
			"ok":        true,
			"plan":      plan,
			"confirmed": rec.Confirmed,
		})
	})
}

func pickPriority(i int) alert.Priority {
	switch i % 3 {
	case 0:
		return alert.PriorityHigh
	case 1:
		return alert.PriorityMed
	default:
		return alert.PriorityLow
	}
}

func pickGroup(i int) string {
	switch i % 4 {
	case 0:
		return "trade"
	case 1:
		return "risk"
	case 2:
		return "price"
	default:
		return "system"
	}
}

func pickDedupKey(i int) string {
	if i%10 == 0 {
		return "dedup-key"
	}
	return ""
}

func pickMergeKey(i int) string {
	if i%7 == 0 {
		return "merge-key"
	}
	return ""
}

func fmtInt(v int) string {
	return strconv.Itoa(v)
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 200, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid limit")
	}
	if v > 1000 {
		return 1000, nil
	}
	return v, nil
}

func parseOffset(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid offset")
	}
	return v, nil
}

func chinaToday() string {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Now().Format("2006-01-02")
	}
	return time.Now().In(loc).Format("2006-01-02")
}

func parseSymbols(raw string, defaults []string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaults
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func extractSymbolFromTitle(title string) string {
	parts := strings.Fields(title)
	if len(parts) > 0 {
		return strings.ToLower(parts[0])
	}
	return ""
}

func ensureIndexSymbol(symbols []string) []string {
	hasIndex := false
	for _, s := range symbols {
		if strings.ToLower(s) == "sh000001" {
			hasIndex = true
			break
		}
	}
	if hasIndex {
		return symbols
	}
	return append([]string{"sh000001"}, symbols...)
}

func applyEvidenceFields(input *riskagent.EventInput, evidenceJSON string) {
	if evidenceJSON == "" {
		return
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(evidenceJSON), &m); err != nil {
		return
	}
	if v, ok := m["change_pct"]; ok {
		input.ChangePct = toFloat(v)
	}
	if v, ok := m["drawdown_pct"]; ok {
		input.DrawdownPct = toFloat(v)
	}
	if v, ok := m["window_sec"]; ok {
		input.WindowSec = int(toFloat(v))
	}
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	}
	return 0
}

package server

import (
	"github.com/compdani/jackery-monitor/internal/forecast"
	"net/http"
	"sort"
	"strconv"
	"time"
)

func pairRuleFirings(rows []map[string]any, now float64) []map[string]any {
	sort.SliceStable(rows, func(i, j int) bool {
		return forecast.Number(rows[i], "fired_at") < forecast.Number(rows[j], "fired_at")
	})
	out := []map[string]any{}
	var opened map[string]any
	for _, r := range rows {
		if r["action"] == "on" && opened == nil {
			opened = r
		} else if r["action"] == "off" && opened != nil {
			out = append(out, ruleInterval(opened, r, forecast.Number(r, "fired_at")))
			opened = nil
		}
	}
	if opened != nil {
		out = append(out, ruleInterval(opened, nil, now))
	}
	return out
}
func ruleInterval(on, off map[string]any, end float64) map[string]any {
	r := map[string]any{"on_at": on["fired_at"], "off_at": nil, "duration_s": end - forecast.Number(on, "fired_at"), "opened_by_rule_id": on["rule_id"], "opened_by_rule_name": on["rule_name"], "closed_by_rule_id": nil, "closed_by_rule_name": nil, "open": off == nil}
	if off != nil {
		r["off_at"], r["closed_by_rule_id"], r["closed_by_rule_name"] = off["fired_at"], off["rule_id"], off["rule_name"]
	}
	return r
}
func (s *Server) ruleHistory(w http.ResponseWriter, r *http.Request, id string) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days == 0 {
		days = 30
	}
	days = max(1, min(365, days))
	now := float64(time.Now().Unix())
	rows, err := s.Store.List("automation_decisions", "", 0, 0)
	if err != nil {
		storageError(w, err)
		return
	}
	firings := []map[string]any{}
	for _, row := range rows {
		if row["rule_id"] == id && forecast.Number(row, "fired_at") >= now-float64(days*86400) {
			firings = append(firings, row)
		}
	}
	sort.SliceStable(firings, func(i, j int) bool {
		return forecast.Number(firings[i], "fired_at") > forecast.Number(firings[j], "fired_at")
	})
	rule, err := s.Store.Get("automation_rules", id)
	if err != nil && !isMissing(err) {
		storageError(w, err)
		return
	}
	host := str(rule, "kasa_host")
	if host == "" && len(firings) > 0 {
		host = str(firings[0], "kasa_host")
	}
	hostRows := []map[string]any{}
	for _, row := range rows {
		if host != "" && row["kasa_host"] == host && forecast.Number(row, "fired_at") >= now-float64(days*86400) {
			hostRows = append(hostRows, row)
		}
	}
	intervals := pairRuleFirings(hostRows, now)
	total := 0.0
	for _, i := range intervals {
		total += forecast.Number(i, "duration_s")
	}
	if len(firings) > 2000 {
		firings = firings[:2000]
	}
	jsonOut(w, 200, map[string]any{"rule_id": id, "rule_exists": rule != nil, "kasa_host": host, "days": days, "firings": firings, "intervals": intervals, "total_on_seconds": total})
}

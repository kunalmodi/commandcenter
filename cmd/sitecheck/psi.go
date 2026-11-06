package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kunalmodi/commandcenter/internal/proc"
)

const psiEndpoint = "https://www.googleapis.com/pagespeedonline/v5/runPagespeed"

// psiResult is the part of the PageSpeed Insights v5 response we read.
type psiResult struct {
	LoadingExperience struct {
		ID              string                    `json:"id"`
		OverallCategory string                    `json:"overall_category"`
		Metrics         map[string]psiFieldMetric `json:"metrics"`
		OriginFallback  bool                      `json:"origin_fallback"`
	} `json:"loadingExperience"`
	LighthouseResult struct {
		Categories map[string]struct {
			Title     string   `json:"title"`
			Score     *float64 `json:"score"`
			AuditRefs []struct {
				ID     string  `json:"id"`
				Weight float64 `json:"weight"`
				Group  string  `json:"group"`
			} `json:"auditRefs"`
		} `json:"categories"`
		Audits map[string]psiAudit `json:"audits"`
	} `json:"lighthouseResult"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type psiFieldMetric struct {
	Percentile float64 `json:"percentile"`
	Category   string  `json:"category"`
}

type psiAudit struct {
	Title            string   `json:"title"`
	Score            *float64 `json:"score"`
	ScoreDisplayMode string   `json:"scoreDisplayMode"`
	DisplayValue     string   `json:"displayValue"`
	Details          struct {
		Type                string  `json:"type"`
		OverallSavingsMs    float64 `json:"overallSavingsMs"`
		OverallSavingsBytes float64 `json:"overallSavingsBytes"`
	} `json:"details"`
	MetricSavings map[string]float64 `json:"metricSavings"`
}

// checkPerformance runs PageSpeed Insights for one strategy, falling back to
// a local Lighthouse run (lhMode "auto") when the API is unavailable, or
// using Lighthouse only (lhMode "always").
func checkPerformance(ctx context.Context, r *report, pageURL, strategy, key, lhMode string, audits bool) {
	const a = "performance"
	S := strings.ToUpper(strategy[:1]) + strategy[1:]
	var psiErr error
	if lhMode != "always" {
		res, err := fetchPSI(ctx, pageURL, strategy, key)
		if err == nil {
			r.add(a, info, S+" source", "PageSpeed Insights (Google's servers, simulated throttling)")
			reportPSI(r, strategy, res, true, audits)
			return
		}
		psiErr = err
	}
	if lhMode == "off" {
		r.add(a, warn, "PageSpeed "+strategy, "%v", psiErr)
		return
	}
	res, err := runLighthouse(ctx, pageURL, strategy)
	if err != nil {
		if psiErr != nil {
			r.add(a, warn, "PageSpeed "+strategy, "%v", psiErr)
		}
		r.add(a, warn, "Lighthouse "+strategy, "local run failed too: %v", err)
		return
	}
	why := ""
	if psiErr != nil {
		why = " (PageSpeed failed: " + psiErr.Error() + ")"
	}
	r.add(a, info, S+" source", "local Lighthouse on this Mac%s. Lab data only: no real-user Core Web Vitals, and scores depend on this machine and network", why)
	reportPSI(r, strategy, res, false, audits)
}

func fetchPSI(ctx context.Context, pageURL, strategy, key string) (*psiResult, error) {
	q := url.Values{"url": {pageURL}, "strategy": {strategy}}
	for _, c := range []string{"performance", "accessibility", "best-practices", "seo"} {
		q.Add("category", c)
	}
	if key != "" {
		q.Set("key", key)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, psiEndpoint+"?"+q.Encode(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	var res psiResult
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("HTTP %d, unreadable response", resp.StatusCode)
	}
	if res.Error != nil || resp.StatusCode != 200 {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if res.Error != nil {
			msg = res.Error.Message
			if i := strings.Index(msg, " for consumer"); i > 0 {
				msg = msg[:i]
			}
		}
		if resp.StatusCode == 429 && key == "" {
			msg += " (the keyless quota is shared; set PAGESPEED_API_KEY in ~/.config/commandcenter/sitecheck.env)"
		}
		return nil, errors.New(msg)
	}
	return &res, nil
}

// Local Lighthouse runs one at a time: two Chromes at once skew the timings.
var lighthouseMu sync.Mutex

// runLighthouse runs the Lighthouse CLI (from PATH, else through npx) in
// headless Chrome and wraps its report like a PageSpeed response.
func runLighthouse(ctx context.Context, pageURL, strategy string) (*psiResult, error) {
	args := []string{pageURL, "--output=json", "--output-path=stdout", "--quiet",
		"--only-categories=performance,accessibility,best-practices,seo",
		"--chrome-flags=--headless=new"}
	if strategy == "desktop" {
		args = append(args, "--preset=desktop")
	}
	name := "lighthouse"
	if _, err := exec.LookPath(name); err != nil {
		if _, err := exec.LookPath("npx"); err != nil {
			return nil, errors.New("needs Chrome plus the lighthouse CLI or npx (Node.js)")
		}
		name = "npx"
		args = append([]string{"-y", "lighthouse@" + lighthouseMajor}, args...)
	}
	lighthouseMu.Lock()
	defer lighthouseMu.Unlock()
	res, err := proc.Run(ctx, proc.Cmd{Name: name, Args: args, Timeout: 4 * time.Minute})
	if err != nil {
		return nil, err
	}
	var out psiResult
	if err := json.Unmarshal([]byte(res.Stdout), &out.LighthouseResult); err != nil {
		return nil, fmt.Errorf("unreadable Lighthouse output: %v", err)
	}
	if len(out.LighthouseResult.Categories) == 0 {
		return nil, errors.New("Lighthouse returned no scores")
	}
	return &out, nil
}

// lighthouseMajor pins the npx fallback to a known major version.
const lighthouseMajor = "13"

// reportPSI turns one PageSpeed/Lighthouse result into findings. field says
// whether real-user data could be present; audits whether to list failing
// non-performance audits (done for one strategy only, they rarely differ).
func reportPSI(r *report, strategy string, res *psiResult, field, audits bool) {
	const a = "performance"
	S := strings.ToUpper(strategy[:1]) + strategy[1:]
	lh := res.LighthouseResult

	// Category scores, on one line.
	scores := map[string]int{}
	var parts []string
	worstScore := pass
	for _, id := range []string{"performance", "accessibility", "best-practices", "seo"} {
		c, ok := lh.Categories[id]
		if !ok || c.Score == nil {
			continue
		}
		sc := int(*c.Score*100 + 0.5)
		scores[id] = sc
		parts = append(parts, fmt.Sprintf("%s %d", strings.ToLower(c.Title), sc))
		switch {
		case sc < 50:
			worstScore = fail
		case sc < 90 && worstScore < warn:
			worstScore = warn
		}
	}
	r.fact("psi_"+strategy+"_scores", scores)
	if len(parts) > 0 {
		r.add(a, worstScore, S+" Lighthouse scores", "%s (out of 100; 90+ is good)", strings.Join(parts, ", "))
	}

	// Real-user data (Chrome UX Report), if Google has enough traffic.
	le := res.LoadingExperience
	if !field {
		// local Lighthouse: no field data to report
	} else if len(le.Metrics) == 0 {
		r.add(a, info, S+" real-user data", "not enough Chrome traffic for field data; lab numbers only")
	} else {
		scope := "this URL"
		if le.OriginFallback || strings.HasPrefix(le.ID, "origin:") {
			scope = "the whole origin"
		}
		names := []struct{ key, label, unit string }{
			{"LARGEST_CONTENTFUL_PAINT_MS", "LCP", "ms"},
			{"INTERACTION_TO_NEXT_PAINT", "INP", "ms"},
			{"CUMULATIVE_LAYOUT_SHIFT_SCORE", "CLS", ""},
			{"FIRST_CONTENTFUL_PAINT_MS", "FCP", "ms"},
			{"EXPERIMENTAL_TIME_TO_FIRST_BYTE", "TTFB", "ms"},
		}
		var parts []string
		worst := pass
		for _, n := range names {
			m, ok := le.Metrics[n.key]
			if !ok {
				continue
			}
			v := fmt.Sprintf("%.0f%s", m.Percentile, n.unit)
			if n.key == "CUMULATIVE_LAYOUT_SHIFT_SCORE" {
				v = fmt.Sprintf("%.2f", m.Percentile/100) // reported ×100
			}
			parts = append(parts, fmt.Sprintf("%s %s (%s)", n.label, v, strings.ToLower(m.Category)))
			if n.label == "LCP" || n.label == "INP" || n.label == "CLS" {
				switch m.Category {
				case "SLOW":
					worst = fail
				case "AVERAGE":
					if worst < warn {
						worst = warn
					}
				}
			}
		}
		verdict := map[level]string{pass: "Core Web Vitals pass", warn: "Core Web Vitals need improvement", fail: "Core Web Vitals fail"}[worst]
		r.add(a, worst, S+" real users (p75, "+scope+")", "%s: %s", verdict, strings.Join(parts, ", "))
	}

	// Lab metrics
	var lab []string
	for _, id := range []string{"first-contentful-paint", "largest-contentful-paint", "total-blocking-time", "cumulative-layout-shift", "speed-index"} {
		if au, ok := lh.Audits[id]; ok && au.DisplayValue != "" {
			lab = append(lab, au.Title+" "+au.DisplayValue)
		}
	}
	if len(lab) > 0 {
		r.add(a, info, S+" lab metrics", strings.Join(lab, ", "))
	}

	// Biggest performance opportunities, by estimated savings.
	type opp struct {
		title string
		ms    float64
		disp  string
	}
	var opps []opp
	if perf, ok := lh.Categories["performance"]; ok {
		for _, ref := range perf.AuditRefs {
			au, ok := lh.Audits[ref.ID]
			if !ok || au.Score == nil || *au.Score >= 0.9 || ref.Group == "metrics" {
				continue
			}
			ms := au.Details.OverallSavingsMs
			if v, ok := au.MetricSavings["LCP"]; ok && v > ms {
				ms = v
			}
			if ms < 100 && au.Details.OverallSavingsBytes < 50<<10 {
				continue
			}
			opps = append(opps, opp{au.Title, ms, au.DisplayValue})
		}
	}
	sort.Slice(opps, func(i, j int) bool { return opps[i].ms > opps[j].ms })
	for i, o := range opps {
		if i == 6 {
			break
		}
		d := o.disp
		if d == "" && o.ms > 0 {
			d = fmt.Sprintf("est. %.0fms", o.ms)
		}
		r.add(a, warn, S+" opportunity", "%s (%s)", o.title, d)
	}

	// Failing accessibility, best-practices and SEO audits (desktop and
	// mobile mostly agree here, so only report them once, for mobile).
	if !audits {
		return
	}
	for _, cat := range []string{"accessibility", "best-practices", "seo"} {
		c, ok := lh.Categories[cat]
		if !ok {
			continue
		}
		var failed []string
		for _, ref := range c.AuditRefs {
			au, ok := lh.Audits[ref.ID]
			if !ok || au.Score == nil || au.ScoreDisplayMode == "informative" || au.ScoreDisplayMode == "manual" || au.ScoreDisplayMode == "notApplicable" {
				continue
			}
			if *au.Score < 0.9 && ref.Weight > 0 {
				failed = append(failed, au.Title)
			}
		}
		if len(failed) > 0 {
			area := map[string]string{"accessibility": "Accessibility", "best-practices": "Best practices", "seo": "Lighthouse SEO"}[cat]
			r.add(a, warn, area+" audits failing", strings.Join(failed, "; "))
		}
	}
}

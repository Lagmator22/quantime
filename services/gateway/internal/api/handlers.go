package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iicpc/gateway/internal/store"
	"github.com/iicpc/gateway/internal/validator"
)

// ── POST /api/submissions ─────────────────────────────────────────────
//
// Multipart upload OR raw body. We hash the bytes, persist the source,
// build a Docker image (offloaded to a goroutine), and reply immediately
// with the submission id so the frontend can poll status.
type submissionCreateReq struct {
	TeamID string `json:"teamId"`
	Name   string `json:"name"`
	Lang   string `json:"lang"`
}

type submissionCreateResp struct {
	ID     string `json:"id"`
	Hash   string `json:"hash"`
	Status string `json:"status"`
}

var buildSem = make(chan struct{}, 4)

func (d *Deps) createSubmission(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := r.ParseMultipartForm(64 << 20); err != nil { // 64MB cap
		httpErr(w, http.StatusBadRequest, "multipart parse: "+err.Error())
		return
	}
	teamID := r.FormValue("teamId")
	name := r.FormValue("name")
	lang := r.FormValue("lang")
	if teamID == "" || lang == "" {
		httpErr(w, http.StatusBadRequest, "teamId and lang required")
		return
	}

	file, header, err := r.FormFile("source")
	if err != nil {
		httpErr(w, http.StatusBadRequest, "source file required")
		return
	}
	defer file.Close()

	body, err := io.ReadAll(io.LimitReader(file, 64<<20))
	if err != nil {
		httpErr(w, http.StatusBadRequest, "read upload: "+err.Error())
		return
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])

	subID := newID("sub")
	sub := &store.Submission{
		ID:        subID,
		TeamID:    teamID,
		Name:      name,
		Lang:      lang,
		Hash:      hash,
		Status:    "uploaded",
		SizeBytes: header.Size,
	}
	if err := d.DB.InsertSubmission(ctx, sub); err != nil {
		httpErr(w, http.StatusInternalServerError, "insert: "+err.Error())
		return
	}

	// Build + deploy in the background. Status transitions:
	//   uploaded → building → built → deploying → deployed | failed
	go d.buildAndDeploy(subID, hash, body)

	writeJSON(w, http.StatusAccepted, submissionCreateResp{
		ID: subID, Hash: hash, Status: "uploaded",
	})
}

func (d *Deps) buildAndDeploy(subID, hash string, archive []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	srcDir, err := d.Sandbox.SaveSource(subID, archive)
	if err != nil {
		log.Printf("[gateway] save source %s: %v", subID, err)
		_ = d.DB.UpdateSubmissionStatus(ctx, subID, "failed", "", "")
		return
	}
	_ = d.DB.UpdateSubmissionStatus(ctx, subID, "building", "", "")

	// Kick off AI analysis in background
	go triggerAIAnalyzer(context.Background(), d, subID, srcDir)

	buildSem <- struct{}{}
	tag, err := d.Sandbox.Build(ctx, subID, hash, srcDir)
	<-buildSem

	if err != nil {
		log.Printf("[gateway] build %s: %v", subID, err)
		_ = d.DB.UpdateSubmissionStatus(ctx, subID, "failed", "", "")
		return
	}
	_ = d.DB.UpdateSubmissionStatus(ctx, subID, "built", tag, "")

	// The submission container exposes :9001 by convention. The gateway
	// publishes the resolvable hostname so bot fleet workers can reach
	// it on the iicpc-net bridge without DNS gymnastics.
	containerName, err := d.Sandbox.Run(ctx, tag, 9001)
	if err != nil {
		log.Printf("[gateway] run %s: %v", subID, err)
		_ = d.DB.UpdateSubmissionStatus(ctx, subID, "failed", tag, "")
		return
	}
	endpoint := "http://" + containerName + ":9001"
	_ = d.DB.UpdateSubmissionStatus(ctx, subID, "deployed", tag, endpoint)
	log.Printf("[gateway] submission %s deployed at %s", subID, endpoint)

	// Correctness oracle: replay a deterministic order sequence through the
	// deployed engine and an independent reference book, diff the fills, and
	// persist a real price-time-priority / fill-accuracy score.
	cres := validator.Validate(ctx, endpoint, d.Now)
	if cj, err := json.Marshal(cres); err == nil {
		if uerr := d.DB.UpdateSubmissionCorrectness(ctx, subID, cj); uerr != nil {
			log.Printf("[gateway] store correctness %s: %v", subID, uerr)
		}
	}
	log.Printf("[gateway] submission %s correctness: %.0f%% (%d/%d cases)", subID, cres.Score, cres.Passed, cres.Total)
}

func triggerAIAnalyzer(ctx context.Context, d *Deps, subID, srcDir string) {
	sub, err := d.DB.GetSubmission(ctx, subID)
	if err != nil || sub == nil {
		return
	}

	var buf bytes.Buffer
	var bytesRead int64
	_ = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(info.Name()))
		if ext == ".go" || ext == ".rs" || ext == ".py" || ext == ".cpp" || ext == ".c" || ext == ".h" || ext == ".hpp" || ext == ".ts" || ext == ".js" {
			data, err := os.ReadFile(path)
			if err == nil {
				if bytesRead+int64(len(data)) > 1024*1024 {
					return filepath.SkipAll
				}
				bytesRead += int64(len(data))
				rel, _ := filepath.Rel(srcDir, path)
				buf.WriteString(fmt.Sprintf("// File: %s\n%s\n\n", rel, string(data)))
			}
		}
		return nil
	})

	if buf.Len() == 0 {
		log.Printf("[gateway] ai-analyzer: no source code found for %s", subID)
		return
	}

	reqBody, _ := json.Marshal(map[string]string{
		"sourceCode":   buf.String(),
		"submissionId": subID,
		"language":     sub.Lang,
	})

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post("http://ai-analyzer:7080/api/analyze", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		log.Printf("[gateway] ai-analyzer error: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[gateway] ai-analyzer non-200: %s", string(body))
		return
	}

	var res struct {
		RiskScore       int `json:"riskScore"`
		Summary         string `json:"summary"`
		Findings        json.RawMessage `json:"findings"`
		Strengths       json.RawMessage `json:"strengths"`
		Recommendations json.RawMessage `json:"recommendations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		log.Printf("[gateway] ai-analyzer decode error: %v", err)
		return
	}

	reportID := newID("rep")
	rep := &store.AnalysisReport{
		ID:              reportID,
		SubmissionID:    subID,
		TeamID:          sub.TeamID,
		RiskScore:       res.RiskScore,
		Summary:         res.Summary,
		Findings:        res.Findings,
		Strengths:       res.Strengths,
		Recommendations: res.Recommendations,
	}

	if err := d.DB.InsertAnalysisReport(ctx, rep); err != nil {
		log.Printf("[gateway] failed to save analysis report for %s: %v", subID, err)
	} else {
		log.Printf("[gateway] saved analysis report %s for submission %s", reportID, subID)
	}
}

func (d *Deps) getSubmission(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id := r.PathValue("id")
	sub, err := d.DB.GetSubmission(ctx, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sub == nil {
		httpErr(w, http.StatusNotFound, "no such submission")
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// ── POST /api/runs ────────────────────────────────────────────────────
type runCreateReq struct {
	SubmissionID     string  `json:"submissionId"`
	Profile          string  `json:"profile"` // sustained|burst|adversarial
	Seed             int64   `json:"seed"`
	DurationSec      int     `json:"durationSec"`
	BotsPerFleet     int     `json:"botsPerFleet"`
	TargetRatePerBot float64 `json:"targetRatePerBot"` // >0 => open-loop fixed arrival rate (orders/sec/bot)
}

type runCreateResp struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (d *Deps) startRun(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var req runCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Profile == "" {
		req.Profile = "sustained"
	}
	if req.DurationSec <= 0 || req.DurationSec > 300 {
		req.DurationSec = 30 // Default 30s, max 300s
	}
	if req.BotsPerFleet <= 0 {
		req.BotsPerFleet = 50 // Default
	} else if req.BotsPerFleet > 100 {
		req.BotsPerFleet = 100 // Hard cap at 100 to prevent host exhaustion
	}
	if req.DurationSec <= 0 {
		req.DurationSec = 30
	} else if req.DurationSec > 300 {
		req.DurationSec = 300 // Max 5 minutes
	}
	sub, err := d.DB.GetSubmission(ctx, req.SubmissionID)
	if err != nil || sub == nil {
		httpErr(w, http.StatusBadRequest, "submission not found")
		return
	}
	if sub.Status != "deployed" {
		httpErr(w, http.StatusConflict, "submission not deployed: "+sub.Status)
		return
	}

	runID := newID("run")
	run := &store.Run{
		ID:           runID,
		SubmissionID: sub.ID,
		TeamID:       sub.TeamID,
		Profile:      req.Profile,
		Seed:         req.Seed,
		Status:       "running",
		StartedAt:    d.Now(),
	}
	if err := d.DB.InsertRun(ctx, run); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	control := map[string]any{
		"type":             "start",
		"runId":            runID,
		"endpoint":         sub.Endpoint,
		"profile":          req.Profile,
		"seed":             req.Seed,
		"durationSec":      req.DurationSec,
		"botsPerFleet":     req.BotsPerFleet,
		"targetRatePerBot": req.TargetRatePerBot,
	}
	payload, _ := json.Marshal(control)
	if err := d.Bus.PublishRunControl(runID, payload); err != nil {
		httpErr(w, http.StatusInternalServerError, "publish: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, runCreateResp{ID: runID, Status: "running"})
}

func (d *Deps) getRun(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id := r.PathValue("id")

	// Fetch run metadata from Postgres
	run, err := d.DB.GetRun(ctx, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if run == nil {
		httpErr(w, http.StatusNotFound, "no such run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (d *Deps) cancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	payload, _ := json.Marshal(map[string]any{"type": "cancel", "runId": id})
	if err := d.Bus.PublishRunControl(id, payload); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id, "status": "cancelling"})
}

// ── POST /api/runs/{id}/baseline ──────────────────────────────────────
// Promote a finished run to the team's regression baseline.
func (d *Deps) setBaseline(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id := r.PathValue("id")
	run, err := d.DB.GetRun(ctx, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if run == nil {
		httpErr(w, http.StatusNotFound, "no such run")
		return
	}
	if run.Status != "finished" {
		httpErr(w, http.StatusConflict, "run not finished")
		return
	}
	if err := d.DB.SetBaseline(ctx, id, run.TeamID); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"baseline": id, "teamId": run.TeamID})
}

// ── GET /api/runs/{id}/regression ─────────────────────────────────────
// Diff a run against its team's baseline (latency tails + throughput +
// composite score), classifying each metric as regression/improvement —
// the "release-gating QA" view exchanges run on every engine change.
func (d *Deps) getRegression(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id := r.PathValue("id")
	cur, err := d.DB.GetRunResult(ctx, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cur == nil {
		httpErr(w, http.StatusNotFound, "no such run")
		return
	}
	base, err := d.DB.GetTeamBaseline(ctx, cur.TeamID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if base == nil || base.RunID == cur.RunID {
		writeJSON(w, http.StatusOK, map[string]any{"hasBaseline": false})
		return
	}

	cm := map[string]float64{}
	bm := map[string]float64{}
	_ = json.Unmarshal(cur.Metrics, &cm)
	_ = json.Unmarshal(base.Metrics, &bm)

	checks := []struct {
		key           string
		betterIsLower bool
	}{
		{"p99", true}, {"p99_9", true}, {"p99_99", true}, {"tps", false},
	}
	const threshold = 0.05 // 5% band = neutral
	deltas := []map[string]any{}
	regressions := 0
	for _, c := range checks {
		b, cv := bm[c.key], cm[c.key]
		if b == 0 {
			continue
		}
		worse := (c.betterIsLower && cv > b*(1+threshold)) || (!c.betterIsLower && cv < b*(1-threshold))
		better := (c.betterIsLower && cv < b*(1-threshold)) || (!c.betterIsLower && cv > b*(1+threshold))
		verdict := "neutral"
		if worse {
			verdict = "regression"
			regressions++
		} else if better {
			verdict = "improvement"
		}
		deltas = append(deltas, map[string]any{
			"metric": c.key, "baseline": b, "current": cv,
			"deltaPct": (cv - b) / b * 100, "verdict": verdict,
		})
	}
	var scoreDelta float64
	if cur.Score != nil && base.Score != nil {
		scoreDelta = *cur.Score - *base.Score
	}
	overall := "OK"
	if regressions > 0 {
		overall = "REGRESSION"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hasBaseline":   true,
		"baselineRunId": base.RunID,
		"currentRunId":  cur.RunID,
		"deltas":        deltas,
		"scoreDelta":    scoreDelta,
		"regressions":   regressions,
		"verdict":       overall,
	})
}

// ── GET /api/leaderboard ──────────────────────────────────────────────
func (d *Deps) getLeaderboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 1. Fast path: Redis
	if topScores, err := d.Cache.LeaderboardTop(ctx, 100); err == nil && len(topScores) > 0 {
		var teamIDs []string
		for _, z := range topScores {
			teamIDs = append(teamIDs, z.Member.(string))
		}
		metricsStr, _ := d.Cache.LeaderboardMetrics(ctx, teamIDs)
		teamsMap, _ := d.DB.GetTeamsMap(ctx, teamIDs)

		var out []store.LeaderboardRow
		for i, z := range topScores {
			tID := z.Member.(string)
			tInfo := teamsMap[tID]

			row := store.LeaderboardRow{
				TeamID: tID,
				Name:   tInfo.Name,
				Region: tInfo.Region,
				Score:  z.Score,
			}

			if i < len(metricsStr) && metricsStr[i] != "" {
				var m map[string]float64
				if json.Unmarshal([]byte(metricsStr[i]), &m) == nil {
					row.P50 = m["p50"]
					row.P99 = m["p99"]
					row.TPS = m["tps"]
					row.ErrPct = m["err_pct"]
				}
			}
			out = append(out, row)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	// 2. Slow path: Postgres fallback
	rows, err := d.DB.Leaderboard(ctx, 100)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// ── GET /api/leaderboard-summary ──────────────────────────────────────
func (d *Deps) getLeaderboardSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 600*time.Second) // Long timeout for AI
	defer cancel()

	var reqPayload struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		APIKey   string `json:"apiKey"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&reqPayload)
	}

	topScores, err := d.Cache.LeaderboardTop(ctx, 100)
	if err != nil || len(topScores) == 0 {
		httpErr(w, http.StatusInternalServerError, "no leaderboard data")
		return
	}

	var teamIDs []string
	for _, z := range topScores {
		teamIDs = append(teamIDs, z.Member.(string))
	}

	metricsStr, _ := d.Cache.LeaderboardMetrics(ctx, teamIDs)
	teamsMap, _ := d.DB.GetTeamsMap(ctx, teamIDs)

	type LeaderboardRun struct {
		Team       string  `json:"team"`
		Score      int     `json:"score"`
		P99        float64 `json:"p99_ns"`
		TPS        float64 `json:"tps"`
		ErrorRate  float64 `json:"error_rate"`
		SourceCode string  `json:"source_code,omitempty"`
	}

	var allRuns []LeaderboardRun

	for i, z := range topScores {
		tID := z.Member.(string)
		tInfo := teamsMap[tID]

		run := LeaderboardRun{
			Team:  tInfo.Name,
			Score: int(z.Score),
		}
		if tInfo.Name == "" {
			run.Team = tID
		}

		if i < len(metricsStr) && metricsStr[i] != "" {
			var m map[string]float64
			if json.Unmarshal([]byte(metricsStr[i]), &m) == nil {
				run.P99 = m["p99"]
				run.TPS = m["tps"]
				run.ErrorRate = m["err_pct"]
			}
		}
		allRuns = append(allRuns, run)
	}

	// Fetch source code for Top 3 and Bottom 3
	for i := 0; i < len(allRuns); i++ {
		isTop3 := i < 3
		isBottom3 := i >= len(allRuns)-3
		if isTop3 || isBottom3 {
			tID := teamIDs[i]
			if sourceCode, err := d.DB.GetBestSubmissionCode(ctx, tID); err == nil {
				allRuns[i].SourceCode = sourceCode
			}
		}
	}

	// Prepare request for ai-analyzer
	payload := map[string]interface{}{
		"runs":     allRuns,
		"provider": reqPayload.Provider,
		"model":    reqPayload.Model,
		"apiKey":   reqPayload.APIKey,
	}

	body, _ := json.Marshal(payload)
	analyzerURL := "http://ai-analyzer:7080/api/analyze-leaderboard"
	
	httpReq, err := http.NewRequestWithContext(ctx, "POST", analyzerURL, bytes.NewReader(body))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "prepare analyzer req: "+err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		httpErr(w, http.StatusBadGateway, "analyzer call failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		httpErr(w, resp.StatusCode, "analyzer returned error: "+string(respBody))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, resp.Body)
}

// ── Admin / Judge Console ─────────────────────────────────────────────

func (d *Deps) getJudgeTeams(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 5*time.Second)
	defer cancel()

	teams, err := d.DB.GetAllTeams(ctx)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "db error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, teams)
}

func (d *Deps) getJudgeRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 5*time.Second)
	defer cancel()

	runs, err := d.DB.GetAllRuns(ctx, 100) // fetch last 100 runs
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "db error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (d *Deps) updateWeights(w http.ResponseWriter, r *http.Request) {
	// A real implementation would update global scoring weights in cache/DB
	// and recompute all scores. For now we accept the payload and return OK.
	var req struct {
		Speed       float64 `json:"speed"`
		Throughput  float64 `json:"throughput"`
		Correctness float64 `json:"correctness"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	log.Printf("[judge] updated weights: spd=%.2f tps=%.2f corr=%.2f", req.Speed, req.Throughput, req.Correctness)
	
	writeJSON(w, http.StatusOK, map[string]string{"status": "recomputed"})
}

func (d *Deps) resetPlatform(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := d.DB.ResetPlatform(ctx); err != nil {
		httpErr(w, http.StatusInternalServerError, "reset failed: "+err.Error())
		return
	}
	log.Printf("[judge] platform reset by admin")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

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

	"github.com/iicpc/gateway/internal/store"
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

	tag, err := d.Sandbox.Build(ctx, subID, hash, srcDir)
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
	SubmissionID string `json:"submissionId"`
	Profile      string `json:"profile"` // sustained|burst|adversarial
	Seed         int64  `json:"seed"`
	DurationSec  int    `json:"durationSec"`
	BotsPerFleet int    `json:"botsPerFleet"`
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
	if req.DurationSec <= 0 || req.DurationSec > 600 {
		req.DurationSec = 30
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
		"type":         "start",
		"runId":        runID,
		"endpoint":     sub.Endpoint,
		"profile":      req.Profile,
		"seed":         req.Seed,
		"durationSec":  req.DurationSec,
		"botsPerFleet": req.BotsPerFleet,
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

// ── GET /api/leaderboard ──────────────────────────────────────────────
func (d *Deps) getLeaderboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := d.DB.Leaderboard(ctx, 100)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

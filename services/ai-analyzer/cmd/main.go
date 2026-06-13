// IICPC AI ANALYZER: HTTP API for AI-powered code analysis
//
// Endpoints:
//   POST /api/analyze         Analyze source code (multipart or JSON body)
//   POST /api/report          Generate post-run performance report
//   GET  /api/health          Service health check
//
// Requires GEMINI_API_KEY env var. Optional GEMINI_MODEL to override model.
// Runs on port 7080 by default (override with PORT env var).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/iicpc/ai-analyzer/internal/agents"
	"github.com/iicpc/ai-analyzer/internal/gemini"
	"github.com/iicpc/ai-analyzer/internal/report"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	log.Println("[ai-analyzer] booting")

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Println("[ai-analyzer] WARNING: GEMINI_API_KEY not set, analysis will fail")
	}

	model := os.Getenv("GEMINI_MODEL")
	client := gemini.NewClient(apiKey, model)

	port := os.Getenv("PORT")
	if port == "" {
		port = "7080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", healthHandler)
	mux.HandleFunc("POST /api/analyze", analyzeHandler(client))
	mux.HandleFunc("POST /api/report", reportHandler(client))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      120 * time.Second, // AI calls can take up to 60s
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("[ai-analyzer] listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[ai-analyzer] http: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("[ai-analyzer] shutdown initiated")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	wg.Wait()
	log.Println("[ai-analyzer] bye")
}

// healthHandler returns service status.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "ai-analyzer",
		"ts":      time.Now().UnixMilli(),
	})
}

// analyzeRequest is the JSON body for POST /api/analyze.
type analyzeRequest struct {
	SourceCode   string `json:"sourceCode"`
	SubmissionID string `json:"submissionId"`
	Language     string `json:"language"`
}

// analyzeHandler runs the multi-agent analysis pipeline on submitted code.
func analyzeHandler(client *gemini.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()

		var sourceCode string

		// Accept either JSON body or multipart file upload
		contentType := r.Header.Get("Content-Type")
		if len(contentType) >= 9 && contentType[:9] == "multipart" {
			// Multipart upload: read "source" file field
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				httpErr(w, http.StatusBadRequest, "multipart parse: "+err.Error())
				return
			}
			file, _, err := r.FormFile("source")
			if err != nil {
				httpErr(w, http.StatusBadRequest, "source file required")
				return
			}
			defer file.Close()
			data, err := io.ReadAll(io.LimitReader(file, 1<<20)) // 1MB cap
			if err != nil {
				httpErr(w, http.StatusBadRequest, "read file: "+err.Error())
				return
			}
			sourceCode = string(data)
		} else {
			// JSON body
			var req analyzeRequest
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
				return
			}
			sourceCode = req.SourceCode
		}

		if len(sourceCode) < 10 {
			httpErr(w, http.StatusBadRequest, "source code too short or missing")
			return
		}

		// Cap source code length to prevent token overflow
		if len(sourceCode) > 100_000 {
			sourceCode = sourceCode[:100_000]
		}

		log.Printf("[ai-analyzer] analyzing %d bytes of source code", len(sourceCode))
		analysisReport, err := agents.Analyze(ctx, client, sourceCode)
		if err != nil {
			log.Printf("[ai-analyzer] analysis error: %v", err)
			httpErr(w, http.StatusInternalServerError, "analysis failed: "+err.Error())
			return
		}

		log.Printf("[ai-analyzer] analysis complete: %d findings, risk=%d", len(analysisReport.Findings), analysisReport.RiskScore)
		writeJSON(w, http.StatusOK, analysisReport)
	}
}

// reportRequest is the JSON body for POST /api/report.
type reportRequest struct {
	SourceCode string         `json:"sourceCode"`
	Metrics    report.Metrics `json:"metrics"`
	RunID      string         `json:"runId"`
}

// reportHandler generates a post-run performance report.
func reportHandler(client *gemini.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()

		var req reportRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if len(req.SourceCode) < 10 {
			httpErr(w, http.StatusBadRequest, "source code required")
			return
		}

		// Cap source code length
		if len(req.SourceCode) > 100_000 {
			req.SourceCode = req.SourceCode[:100_000]
		}

		log.Printf("[ai-analyzer] generating report for run %s", req.RunID)
		perfReport, err := report.GeneratePerformanceReport(ctx, client, req.SourceCode, req.Metrics)
		if err != nil {
			log.Printf("[ai-analyzer] report error: %v", err)
			httpErr(w, http.StatusInternalServerError, "report generation failed: "+err.Error())
			return
		}

		log.Printf("[ai-analyzer] report complete for run %s", req.RunID)
		writeJSON(w, http.StatusOK, perfReport)
	}
}

// Middleware: CORS + access logging + panic recovery
func withMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Panic recovery
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[ai-analyzer] PANIC %s %s: %v", r.Method, r.URL.Path, rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()

		// CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Access log
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		h.ServeHTTP(sw, r)
		log.Printf("[ai-analyzer] %d %s %s %v", sw.status, r.Method, r.URL.Path, time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[ai-analyzer] write json: %v", err)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

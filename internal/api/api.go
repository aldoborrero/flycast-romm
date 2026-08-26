// Package api serves the HTTP contract RomM drives, and nothing else.
//
// The routes, request bodies, response shapes and status codes here are the
// ones reconstructed in docs/CONTRACT.md from RomM's backend. Two of them are
// checked by RomM on a literal string rather than the status code —
// /save-state must answer `"status": "saving"` and /volume `"status": "ok"` —
// so the responses are not free to drift.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/aldoborrero/flycast-romm/internal/config"
	"github.com/aldoborrero/flycast-romm/internal/flycast"
	"github.com/aldoborrero/flycast-romm/internal/session"
)

// maxBodyBytes matches the sibling brokers' 64 KB cap. Every body in this
// contract is a handful of fields; anything larger is a mistake or an attack.
const maxBodyBytes = 64 * 1024

type Server struct {
	cfg  config.Config
	log  *slog.Logger
	ctl  flycast.Controller
	sess *session.Manager
}

func NewServer(cfg config.Config, log *slog.Logger, ctl flycast.Controller, sess *session.Manager) *Server {
	return &Server{cfg: cfg, log: log, ctl: ctl, sess: sess}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// /health is deliberately unauthenticated: it is what a container
	// healthcheck calls, and those cannot carry the shared secret.
	mux.HandleFunc("GET /health", s.handleHealth)

	mux.Handle("GET /status", s.authed(s.handleStatus))
	mux.Handle("POST /launch", s.authed(s.handleLaunch))
	mux.Handle("DELETE /launch", s.authed(s.handleStop))
	mux.Handle("POST /save-and-exit", s.authed(s.handleSaveAndExit))
	mux.Handle("POST /save-state", s.authed(s.handleSaveState))
	mux.Handle("POST /load-state", s.authed(s.handleLoadState))
	mux.Handle("POST /volume", s.authed(s.handleVolume))
	mux.Handle("POST /mute", s.authed(s.handleMute))

	// The sibling brokers answer JSON everywhere, including on a wrong path,
	// so a caller never has to guess whether it reached the broker at all.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.refuse(w, r, http.StatusNotFound, "not found", "no such route")
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("handler panicked", "method", r.Method, "path", r.URL.Path, "panic", rec)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal broker error"})
			}
		}()
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) authed(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.secretOK(r) {
			s.refuse(w, r, http.StatusForbidden, "forbidden", "bad or missing X-Broker-Secret")
			return
		}
		next(w, r)
	})
}

func (s *Server) secretOK(r *http.Request) bool {
	if s.cfg.Secret == "" {
		return true
	}
	got := r.Header.Get("X-Broker-Secret")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Secret)) == 1
}

// ── Responses ────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, body map[string]any) {
	payload, err := json.Marshal(body)
	if err != nil {
		payload = []byte(`{"error":"internal broker error"}`)
		code = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
	w.WriteHeader(code)
	_, _ = w.Write(payload)
}

// refuse answers with an error and logs the reason. Every refusal is logged as
// well as returned, so `docker logs` alone explains a failure without turning
// on debug: a 4xx is the caller's mistake, a 5xx is ours.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, code int, msg, reason string, extra ...map[string]any) {
	body := map[string]any{"error": msg}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	level := slog.LevelWarn
	if code >= 500 {
		level = slog.LevelError
	}
	s.log.Log(r.Context(), level, "refused request",
		"code", code, "method", r.Method, "path", r.URL.Path,
		"from", r.RemoteAddr, "reason", reason)
	writeJSON(w, code, body)
}

// ── Request bodies ───────────────────────────────────────────────────────────

type body map[string]json.RawMessage

// readBody parses the request body as a JSON object. It returns ok=false once
// an error response has already been written. An absent body is an empty
// object, matching the sibling brokers: POST /mute with no body means toggle.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request) (body, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "could not read request body", err.Error())
		return nil, false
	}
	if len(raw) > maxBodyBytes {
		s.refuse(w, r, http.StatusRequestEntityTooLarge, "request body too large",
			fmt.Sprintf("body exceeds %d bytes", maxBodyBytes))
		return nil, false
	}
	if len(raw) == 0 {
		return body{}, true
	}

	var b body
	if err := json.Unmarshal(raw, &b); err != nil {
		// Malformed JSON used to read back as {} in an earlier broker, which
		// turned a typo'd /launch into a silent idle relaunch. It is an error.
		s.refuse(w, r, http.StatusBadRequest, "body must be a JSON object", err.Error())
		return nil, false
	}
	return b, true
}

func (b body) has(key string) bool { _, ok := b[key]; return ok }

func (b body) str(key string) (string, error) {
	raw, ok := b[key]
	if !ok {
		return "", nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return v, nil
}

func (b body) intOr(key string, def int) (int, error) {
	raw, ok := b[key]
	if !ok {
		return def, nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return v, nil
}

func (b body) boolOr(key string, def bool) (bool, error) {
	raw, ok := b[key]
	if !ok {
		return def, nil
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return v, nil
}

// ── Slot validation ──────────────────────────────────────────────────────────

// slotFrom reads and validates a RomM slot. Zero means "the autosave slot",
// the same alias every broker in this family implements, and is translated by
// the caller through config.FlycastSlot.
func (s *Server) slotFrom(w http.ResponseWriter, r *http.Request, b body, def int) (int, bool) {
	slot, err := b.intOr("slot", def)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, err.Error(), err.Error())
		return 0, false
	}
	if slot < 0 || slot > config.MaxSlot {
		reason := fmt.Sprintf("slot must be 0-%d", config.MaxSlot)
		s.refuse(w, r, http.StatusBadRequest, reason, fmt.Sprintf("got slot %d", slot))
		return 0, false
	}
	return slot, true
}

// sessionRefusal maps the session package's guards onto the 409s the sibling
// brokers return, so RomM and any operator script see identical behaviour.
func (s *Server) sessionRefusal(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, session.ErrNoGame),
		errors.Is(err, session.ErrSaveInProgress),
		errors.Is(err, session.ErrLaunchInProgress),
		errors.Is(err, session.ErrStopInProgress):
		s.refuse(w, r, http.StatusConflict, err.Error(), err.Error())
	default:
		s.refuse(w, r, http.StatusInternalServerError, "internal broker error", err.Error())
	}
	return true
}

func rfc3339(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

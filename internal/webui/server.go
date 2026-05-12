package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	client cerbapi.Client
	logger *slog.Logger
}

func New(client cerbapi.Client, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{client: client, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/resources", s.handleResources)
	mux.HandleFunc("/api/resources/", s.handleResourceByID)

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(staticSub))
	mux.Handle("/", fileServer)
	return s.withLogging(mux)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.logger.Info("webui.request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := s.client.ListResources(r.Context(), cerbapi.ResourceListArgs{
		ProjectID: r.URL.Query().Get("project"),
		Connector: r.URL.Query().Get("connector"),
		Tag:       r.URL.Query().Get("tag"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleResourceByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/resources/")
	if rest == "" {
		writeError(w, http.StatusBadRequest, "resource id required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	switch action {
	case "":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := s.client.GetResourceRuntime(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "logs":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		lines := 100
		if raw := r.URL.Query().Get("lines"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				lines = n
			}
		}
		stream := r.URL.Query().Get("stream")
		out, err := s.client.ResourceLogs(r.Context(), id, lines, stream)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "apply", "deploy", "reload":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := s.performAction(r.Context(), id, action)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown resource action %q", action))
	}
}

func (s *Server) performAction(ctx context.Context, id, action string) (*cerbapi.OpResult, error) {
	switch action {
	case "apply":
		return s.client.ApplyResource(ctx, id)
	case "deploy":
		return s.client.DeployResource(ctx, id)
	case "reload":
		return s.client.ReloadResource(ctx, id)
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"success": false,
		"error":   msg,
	})
}

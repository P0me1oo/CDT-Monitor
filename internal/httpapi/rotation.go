package httpapi

import (
	"github.com/P0me1oo/CDT-Monitor/internal/domain"
	"net/http"
)

func (s *Server) getRotation(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetRotation(r.Context())
	if err != nil {
		writeError(w, 500, "rotation_failed", err.Error())
		return
	}
	cfg.Token = ""
	state, err := s.store.GetRotationState(r.Context())
	if err != nil {
		writeError(w, 500, "rotation_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"config": cfg, "state": state})
}

func (s *Server) saveRotation(w http.ResponseWriter, r *http.Request) {
	var cfg domain.RotationConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if err := s.engine.SaveRotation(r.Context(), cfg); err != nil {
		writeError(w, 400, "rotation_failed", err.Error())
		return
	}
	_ = s.store.AddLog(r.Context(), "audit", "管理员更新轮换运行配置")
	s.getRotation(w, r)
}

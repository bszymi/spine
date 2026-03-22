package gateway

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/go-chi/chi/v5"
)

type createTokenRequest struct {
	ActorID   string  `json:"actor_id"`
	Name      string  `json:"name"`
	ExpiresIn *string `json:"expires_in,omitempty"` // duration string, e.g. "720h"
}

type createTokenResponse struct {
	TokenID   string     `json:"token_id"`
	Token     string     `json:"token"` // plaintext, shown only once
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "token.create") {
		return
	}

	if s.auth == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "auth not configured"))
		return
	}

	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "invalid request body"))
		return
	}
	if req.ActorID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "actor_id required"))
		return
	}

	var expiresAt *time.Time
	if req.ExpiresIn != nil {
		d, err := time.ParseDuration(*req.ExpiresIn)
		if err != nil {
			WriteError(w, domain.NewError(domain.ErrInvalidParams, "invalid expires_in duration"))
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	plaintext, tokenID, err := s.auth.CreateToken(r.Context(), req.ActorID, req.Name, expiresAt)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, createTokenResponse{
		TokenID:   tokenID,
		Token:     plaintext,
		ExpiresAt: expiresAt,
	})
}

func (s *Server) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "token.revoke") {
		return
	}

	if s.auth == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "auth not configured"))
		return
	}

	tokenID := chi.URLParam(r, "token_id")
	if err := s.auth.RevokeToken(r.Context(), tokenID); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) handleTokenList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "token.list") {
		return
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	actorID := r.URL.Query().Get("actor_id")
	if actorID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "actor_id query parameter required"))
		return
	}

	tokens, err := s.store.ListTokensByActor(r.Context(), actorID)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"items": tokens})
}

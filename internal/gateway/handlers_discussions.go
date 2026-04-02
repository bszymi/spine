package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/go-chi/chi/v5"
)

// ── Request / Response Types ──

type createThreadRequest struct {
	AnchorType string `json:"anchor_type"`
	AnchorID   string `json:"anchor_id"`
	TopicKey   string `json:"topic_key,omitempty"`
	Title      string `json:"title,omitempty"`
}

type createCommentRequest struct {
	Content         string          `json:"content"`
	ParentCommentID string          `json:"parent_comment_id,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type resolveThreadRequest struct {
	ResolutionType string   `json:"resolution_type"`
	ResolutionRefs []string `json:"resolution_refs,omitempty"`
}

type threadResponse struct {
	ThreadID       string              `json:"thread_id"`
	AnchorType     domain.AnchorType   `json:"anchor_type"`
	AnchorID       string              `json:"anchor_id"`
	TopicKey       string              `json:"topic_key,omitempty"`
	Title          string              `json:"title,omitempty"`
	Status         domain.ThreadStatus `json:"status"`
	CreatedBy      string              `json:"created_by"`
	CreatedAt      time.Time           `json:"created_at"`
	ResolvedAt     *time.Time          `json:"resolved_at,omitempty"`
	ResolutionType string              `json:"resolution_type,omitempty"`
	ResolutionRefs json.RawMessage     `json:"resolution_refs,omitempty"`
	Comments       []domain.Comment    `json:"comments,omitempty"`
	TraceID        string              `json:"trace_id"`
}

func threadToResponse(t *domain.DiscussionThread, traceID string) threadResponse {
	return threadResponse{
		ThreadID:       t.ThreadID,
		AnchorType:     t.AnchorType,
		AnchorID:       t.AnchorID,
		TopicKey:       t.TopicKey,
		Title:          t.Title,
		Status:         t.Status,
		CreatedBy:      t.CreatedBy,
		CreatedAt:      t.CreatedAt,
		ResolvedAt:     t.ResolvedAt,
		ResolutionType: string(t.ResolutionType),
		ResolutionRefs: t.ResolutionRefs,
		TraceID:        traceID,
	}
}

// ── Handlers ──

// POST /api/v1/discussions
func (s *Server) handleDiscussionCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "discussion.create") {
		return
	}
	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	var req createThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.AnchorType == "" || req.AnchorID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "anchor_type and anchor_id required"))
		return
	}

	anchorType := domain.AnchorType(req.AnchorType)
	if !isValidAnchorType(anchorType) {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "anchor_type must be one of: artifact, run, step_execution, divergence_context"))
		return
	}

	// Validate the anchor target exists
	if err := s.validateAnchorExists(r.Context(), anchorType, req.AnchorID); err != nil {
		WriteError(w, err)
		return
	}

	actor := actorFromContext(r.Context())
	createdBy := ""
	if actor != nil {
		createdBy = actor.ActorID
	}

	traceID := observe.TraceID(r.Context())
	threadID, err := generateID("thread")
	if err != nil {
		WriteError(w, domain.NewError(domain.ErrInternal, "failed to generate thread ID"))
		return
	}

	thread := &domain.DiscussionThread{
		ThreadID:   threadID,
		AnchorType: anchorType,
		AnchorID:   req.AnchorID,
		TopicKey:   req.TopicKey,
		Title:      req.Title,
		Status:     domain.ThreadStatusOpen,
		CreatedBy:  createdBy,
		CreatedAt:  time.Now().UTC(),
	}

	if err := s.storeFrom(r.Context()).CreateThread(r.Context(), thread); err != nil {
		WriteError(w, err)
		return
	}

	s.emitDiscussionEvent(r.Context(), domain.EventThreadCreated, thread.ThreadID, map[string]any{
		"anchor_type": thread.AnchorType,
		"anchor_id":   thread.AnchorID,
		"created_by":  thread.CreatedBy,
	})

	WriteJSON(w, http.StatusCreated, threadToResponse(thread, traceID))
}

// GET /api/v1/discussions
func (s *Server) handleDiscussionList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "discussion.list") {
		return
	}
	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	anchorType := r.URL.Query().Get("anchor_type")
	anchorID := r.URL.Query().Get("anchor_id")
	if anchorType == "" || anchorID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "anchor_type and anchor_id query parameters required"))
		return
	}

	at := domain.AnchorType(anchorType)
	if !isValidAnchorType(at) {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "invalid anchor_type"))
		return
	}

	threads, err := s.storeFrom(r.Context()).ListThreads(r.Context(), at, anchorID)
	if err != nil {
		WriteError(w, err)
		return
	}

	// Optional status filter
	if statusFilter := r.URL.Query().Get("status"); statusFilter != "" {
		filtered := threads[:0]
		for i := range threads {
			if string(threads[i].Status) == statusFilter {
				filtered = append(filtered, threads[i])
			}
		}
		threads = filtered
	}

	WriteJSON(w, http.StatusOK, map[string]any{"items": threads})
}

// GET /api/v1/discussions/{thread_id}
func (s *Server) handleDiscussionGet(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "discussion.get") {
		return
	}
	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	threadID := chi.URLParam(r, "thread_id")
	thread, err := s.storeFrom(r.Context()).GetThread(r.Context(), threadID)
	if err != nil {
		WriteError(w, err)
		return
	}

	comments, err := s.storeFrom(r.Context()).ListComments(r.Context(), threadID)
	if err != nil {
		WriteError(w, err)
		return
	}

	traceID := observe.TraceID(r.Context())
	resp := threadToResponse(thread, traceID)
	resp.Comments = comments
	WriteJSON(w, http.StatusOK, resp)
}

// POST /api/v1/discussions/{thread_id}/comments
func (s *Server) handleDiscussionComment(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "discussion.comment") {
		return
	}
	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	threadID := chi.URLParam(r, "thread_id")

	// Verify thread exists and is open
	thread, err := s.storeFrom(r.Context()).GetThread(r.Context(), threadID)
	if err != nil {
		WriteError(w, err)
		return
	}
	if thread.Status != domain.ThreadStatusOpen {
		WriteError(w, domain.NewError(domain.ErrPrecondition, fmt.Sprintf("thread is %s, must be open to comment", thread.Status)))
		return
	}

	var req createCommentRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Content == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "content required"))
		return
	}

	actor := actorFromContext(r.Context())
	authorID := ""
	authorType := "human"
	if actor != nil {
		authorID = actor.ActorID
		authorType = string(actor.Type)
	}

	commentID, err := generateID("comment")
	if err != nil {
		WriteError(w, domain.NewError(domain.ErrInternal, "failed to generate comment ID"))
		return
	}

	comment := &domain.Comment{
		CommentID:       commentID,
		ThreadID:        threadID,
		ParentCommentID: req.ParentCommentID,
		AuthorID:        authorID,
		AuthorType:      authorType,
		Content:         req.Content,
		Metadata:        req.Metadata,
		CreatedAt:       time.Now().UTC(),
	}

	if err := s.storeFrom(r.Context()).CreateComment(r.Context(), comment); err != nil {
		WriteError(w, err)
		return
	}

	s.emitDiscussionEvent(r.Context(), domain.EventCommentAdded, threadID, map[string]any{
		"comment_id":  comment.CommentID,
		"author_id":   comment.AuthorID,
		"author_type": comment.AuthorType,
	})

	WriteJSON(w, http.StatusCreated, comment)
}

// POST /api/v1/discussions/{thread_id}/resolve
func (s *Server) handleDiscussionResolve(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "discussion.resolve") {
		return
	}
	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	threadID := chi.URLParam(r, "thread_id")
	thread, err := s.storeFrom(r.Context()).GetThread(r.Context(), threadID)
	if err != nil {
		WriteError(w, err)
		return
	}

	if thread.Status != domain.ThreadStatusOpen {
		WriteError(w, domain.NewError(domain.ErrPrecondition, fmt.Sprintf("thread is %s, must be open to resolve", thread.Status)))
		return
	}

	var req resolveThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}

	now := time.Now().UTC()
	thread.Status = domain.ThreadStatusResolved
	thread.ResolvedAt = &now
	thread.ResolutionType = domain.ResolutionType(req.ResolutionType)
	if len(req.ResolutionRefs) > 0 {
		refs, err := json.Marshal(req.ResolutionRefs)
		if err != nil {
			WriteError(w, domain.NewError(domain.ErrInvalidParams, "invalid resolution_refs"))
			return
		}
		thread.ResolutionRefs = refs
	}

	if err := s.storeFrom(r.Context()).UpdateThread(r.Context(), thread); err != nil {
		WriteError(w, err)
		return
	}

	s.emitDiscussionEvent(r.Context(), domain.EventThreadResolved, thread.ThreadID, map[string]any{
		"resolution_type": string(thread.ResolutionType),
		"resolution_refs": thread.ResolutionRefs,
	})

	traceID := observe.TraceID(r.Context())
	WriteJSON(w, http.StatusOK, threadToResponse(thread, traceID))
}

// POST /api/v1/discussions/{thread_id}/reopen
func (s *Server) handleDiscussionReopen(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "discussion.reopen") {
		return
	}
	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	threadID := chi.URLParam(r, "thread_id")
	thread, err := s.storeFrom(r.Context()).GetThread(r.Context(), threadID)
	if err != nil {
		WriteError(w, err)
		return
	}

	if thread.Status == domain.ThreadStatusOpen {
		WriteError(w, domain.NewError(domain.ErrPrecondition, "thread is already open"))
		return
	}

	thread.Status = domain.ThreadStatusOpen
	thread.ResolvedAt = nil
	thread.ResolutionType = ""
	thread.ResolutionRefs = nil

	if err := s.storeFrom(r.Context()).UpdateThread(r.Context(), thread); err != nil {
		WriteError(w, err)
		return
	}

	traceID := observe.TraceID(r.Context())
	WriteJSON(w, http.StatusOK, threadToResponse(thread, traceID))
}

// GET /api/v1/query/discussions — list open discussions for a run's artifacts
func (s *Server) handleQueryDiscussions(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "discussion.list") {
		return
	}
	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	runID := r.URL.Query().Get("run_id")
	if runID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "run_id query parameter required"))
		return
	}

	run, err := s.storeFrom(r.Context()).GetRun(r.Context(), runID)
	if err != nil {
		WriteError(w, err)
		return
	}

	// List threads anchored to the run's task artifact
	artifactThreads, err := s.storeFrom(r.Context()).ListThreads(r.Context(), domain.AnchorTypeArtifact, run.TaskPath)
	if err != nil {
		WriteError(w, err)
		return
	}

	// List threads anchored directly to the run
	runThreads, err := s.storeFrom(r.Context()).ListThreads(r.Context(), domain.AnchorTypeRun, runID)
	if err != nil {
		WriteError(w, err)
		return
	}

	artifactThreads = append(artifactThreads, runThreads...)
	allThreads := artifactThreads

	// Optional status filter (default: open)
	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = "open"
	}
	if statusFilter != "all" {
		filtered := allThreads[:0]
		for i := range allThreads {
			if string(allThreads[i].Status) == statusFilter {
				filtered = append(filtered, allThreads[i])
			}
		}
		allThreads = filtered
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"items":  allThreads,
		"run_id": runID,
	})
}

// ── Helpers ──

func generateID(prefix string) (string, error) {
	id, err := observe.GenerateTraceID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", prefix, id[:8]), nil
}

func isValidAnchorType(t domain.AnchorType) bool {
	for _, valid := range domain.ValidAnchorTypes() {
		if t == valid {
			return true
		}
	}
	return false
}

func (s *Server) emitDiscussionEvent(ctx context.Context, eventType domain.EventType, threadID string, extra map[string]any) {
	if s.events == nil {
		return
	}
	extra["thread_id"] = threadID
	payload, _ := json.Marshal(extra)
	evtID, _ := generateID("evt")
	if err := s.events.Emit(ctx, domain.Event{
		EventID:   evtID,
		Type:      eventType,
		Timestamp: time.Now(),
		TraceID:   observe.TraceID(ctx),
		Payload:   payload,
	}); err != nil {
		observe.Logger(ctx).Warn("failed to emit discussion event",
			"event_type", eventType,
			"thread_id", threadID,
			"error", err,
		)
	}
}

func (s *Server) validateAnchorExists(ctx context.Context, anchorType domain.AnchorType, anchorID string) error {
	var err error
	switch anchorType {
	case domain.AnchorTypeArtifact:
		_, err = s.storeFrom(ctx).GetArtifactProjection(ctx, anchorID)
	case domain.AnchorTypeRun:
		_, err = s.storeFrom(ctx).GetRun(ctx, anchorID)
	case domain.AnchorTypeStepExecution:
		_, err = s.storeFrom(ctx).GetStepExecution(ctx, anchorID)
	case domain.AnchorTypeDivergenceContext:
		_, err = s.storeFrom(ctx).GetDivergenceContext(ctx, anchorID)
	}
	return err
}

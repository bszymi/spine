package gateway

import "net/http"

func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.start") {
		return
	}
	WriteNotImplemented(w)
}

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.status") {
		return
	}
	WriteNotImplemented(w)
}

func (s *Server) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "run.cancel") {
		return
	}
	WriteNotImplemented(w)
}

func (s *Server) handleStepSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "step.submit") {
		return
	}
	WriteNotImplemented(w)
}

func (s *Server) handleStepAssign(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "step.assign") {
		return
	}
	WriteNotImplemented(w)
}

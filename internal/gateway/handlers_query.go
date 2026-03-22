package gateway

import "net/http"

func (s *Server) handleQueryArtifacts(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "query.artifacts") {
		return
	}
	WriteNotImplemented(w)
}

func (s *Server) handleQueryGraph(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "query.graph") {
		return
	}
	WriteNotImplemented(w)
}

func (s *Server) handleQueryHistory(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "query.history") {
		return
	}
	WriteNotImplemented(w)
}

func (s *Server) handleQueryRuns(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "query.runs") {
		return
	}
	WriteNotImplemented(w)
}

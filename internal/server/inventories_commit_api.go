package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// commitHelpers are the shared helpers of inventoryRoutes used by the Git
// write-back handlers.
type commitHelpers struct {
	target  func(http.ResponseWriter, *http.Request, policy.Permission) (policy.Decision, uuid.UUID, bool)
	ifMatch func(http.ResponseWriter, *http.Request) (*int, bool)
	decode  func(http.ResponseWriter, *http.Request, any) bool
	audit   func(*http.Request, policy.Decision, string, uuid.UUID, map[string]any)
	etag    func(http.ResponseWriter, int)
	fail    func(http.ResponseWriter, string, error)
}

// registerInventoryCommitRoutes mounts POST {item}/commit and GET {item}/diff
// (ADR-0014 write-back).
// h.target must only pass when the caller's scope covers the whole inventory.
func registerInventoryCommitRoutes(r chi.Router, az *policy.Authorizer, svc InventoriesAPI, item string, h commitHelpers) {
	az.Handle(r, http.MethodPost, item+"/commit", policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := h.target(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		match, ok := h.ifMatch(w, req)
		if !ok {
			return
		}
		var body struct {
			Message string `json:"message"`
		}
		if req.ContentLength != 0 && !h.decode(w, req, &body) {
			return
		}
		u := auth.FromContext(req.Context()).User
		res, err := svc.Commit(req.Context(), d.Workspace.ID, id,
			inventory.CommitInput{Message: body.Message, Author: projects.Author{Name: u.DisplayName, Email: u.Email}}, match)
		if err != nil {
			h.fail(w, "commit inventory", err)
			return
		}
		if !res.NoOp {
			h.audit(req, d, "inventory.commit", id, map[string]any{"branch": res.Branch, "sha": res.SHA, "paths": res.Paths})
		}
		h.etag(w, res.Inventory.Version)
		policy.WriteJSON(w, http.StatusOK, res)
	})

	az.Handle(r, http.MethodGet, item+"/diff", policy.InventoryRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := h.target(w, req, policy.InventoryRead)
		if !ok {
			return
		}
		out, err := svc.Diff(req.Context(), d.Workspace.ID, id)
		if err != nil {
			h.fail(w, "diff inventory", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, out)
	})
}

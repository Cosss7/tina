package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"

	"github.com/Cosss7/tina/services/control-plane/internal/project"
)

type API struct {
	projects *project.Service
	hub      *Hub
}

func newHandler(projects *project.Service, hub *Hub, static fs.FS) http.Handler {
	a := &API{projects: projects, hub: hub}
	if registrations, err := projects.Registrations(context.Background()); err == nil {
		for _, registration := range registrations {
			copy := registration
			hub.mu.Lock()
			if hub.nodes[copy.NodeID] == nil {
				hub.nodes[copy.NodeID] = &Node{ID: copy.NodeID, Name: copy.Name, Version: copy.Version, Registration: &copy}
			}
			hub.mu.Unlock()
		}
	} else {
		log.Printf("Restore node directory: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "version": version})
	})
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("GET /ws/node", hub.ServeNodeWS)
	mux.HandleFunc("GET /ws/tinad", a.serveExecution)
	mux.HandleFunc("POST /api/nodes/enroll", a.enrollNode)
	mux.HandleFunc("PUT /api/projects/{id}/memory", a.routerMemory)
	mux.HandleFunc("POST /api/projects", a.createProject)
	mux.HandleFunc("GET /api/projects/{id}/agents", func(w http.ResponseWriter, r *http.Request) {
		v, err := projects.Agents(r.Context(), r.PathValue("id"))
		a.reply(w, v, err, 200)
	})
	mux.HandleFunc("PUT /api/projects/{id}/agents/{agent}", a.configureAgent)
	mux.HandleFunc("GET /api/board", func(w http.ResponseWriter, r *http.Request) {
		v, err := projects.Board(r.Context(), r.URL.Query().Get("project"))
		a.reply(w, v, err, 200)
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		v, err := projects.Events(r.Context(), r.URL.Query().Get("project"))
		a.reply(w, v, err, 200)
	})
	mux.HandleFunc("GET /api/queue", func(w http.ResponseWriter, r *http.Request) {
		v, err := projects.Queue(r.Context(), r.URL.Query().Get("project"))
		a.reply(w, v, err, 200)
	})
	mux.HandleFunc("POST /api/cards", a.createCard)
	mux.HandleFunc("POST /api/plans", a.requestPlan)
	mux.HandleFunc("POST /api/plans/{id}/decide", a.decidePlan)
	mux.HandleFunc("POST /api/tasks/{id}/dispatch", a.dispatchTask)
	mux.HandleFunc("POST /api/tasks/{id}/review", a.reviewTask)
	mux.Handle("GET /", http.FileServerFS(static))
	return mux
}
func (a *API) state(w http.ResponseWriter, r *http.Request) {
	projects, err := a.projects.Projects(r.Context())
	if err != nil {
		a.reply(w, nil, err, 200)
		return
	}
	writeJSON(w, struct {
		Snapshot
		Projects []project.Project `json:"projects"`
	}{a.hub.Snapshot(), projects})
}
func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	v, err := a.projects.CreateProject(r.Context(), "", stringField(body, "name"), "human:local")
	a.reply(w, v, err, 201)
}
func (a *API) configureAgent(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	selection := project.EngineSelection{Model: stringField(body, "model")}
	if options, exists := body["options"]; exists && options != nil {
		raw, err := json.Marshal(options)
		if err != nil || json.Unmarshal(raw, &selection.Options) != nil {
			writeError(w, 400, "invalid_argument", "options must be a JSON object")
			return
		}
	}
	v, err := a.projects.ConfigureAgent(r.Context(), r.PathValue("id"), r.PathValue("agent"), stringField(body, "name"), stringField(body, "engineId"), stringField(body, "nodeId"), stringField(body, "cwd"), "human:local", selection)
	a.reply(w, v, err, 200)
}
func (a *API) createCard(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	v, err := a.projects.CreateDemand(r.Context(), stringField(body, "projectId"), stringField(body, "title"), stringField(body, "description"), "human:local")
	a.reply(w, v, err, 201)
}
func (a *API) requestPlan(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	v, err := a.projects.RequestPlan(r.Context(), stringField(body, "projectId"), stringField(body, "taskId"), "human:local")
	a.reply(w, v, err, 202)
}
func (a *API) decidePlan(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	v, err := a.projects.DecidePlan(r.Context(), stringField(body, "projectId"), r.PathValue("id"), stringField(body, "decision"), "human:local")
	a.reply(w, v, err, 200)
}
func (a *API) dispatchTask(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	v, err := a.projects.Dispatch(r.Context(), stringField(body, "projectId"), r.PathValue("id"), "human:local")
	a.reply(w, v, err, 202)
}
func (a *API) reviewTask(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	v, err := a.projects.Review(r.Context(), stringField(body, "projectId"), r.PathValue("id"), stringField(body, "decision"), stringField(body, "note"), "human:local")
	a.reply(w, v, err, 200)
}
func (a *API) reply(w http.ResponseWriter, value any, err error, status int) {
	if err != nil {
		var business *project.Error
		if errors.As(err, &business) {
			status := http.StatusBadRequest
			switch business.Code {
			case "not_found":
				status = 404
			case "conflict":
				status = 409
			}
			writeError(w, status, business.Code, business.Message)
		} else {
			log.Printf("Project API failed: %v", err)
			writeError(w, 500, "internal", "Project operation failed")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	writeJSON(w, value)
}
func decodeBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var body map[string]any
	if err := decoder.Decode(&body); err != nil || body == nil {
		writeError(w, 400, "invalid_argument", "Request body must be a JSON object of at most 1 MiB")
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(w, 400, "invalid_argument", "Expected one JSON object")
		return nil, false
	}
	return body, true
}
func stringField(body map[string]any, key string) string {
	value, _ := body[key].(string)
	return value
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

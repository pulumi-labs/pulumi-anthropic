// Copyright 2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package fakeapi is an in-memory stand-in for the Anthropic API that implements its
// uniform REST conventions, for testing the provider without credentials. It records
// every request so tests can assert on exactly what the provider sent.
//
// The fake is a proxy for the real API, not a substitute: behavior that only the real
// API can confirm is covered by the acceptance tests under test/.
package fakeapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Object is an API object as the fake stores it.
type Object = map[string]any

// Request is a request the fake received.
type Request struct {
	Method string
	Path   string
	Header http.Header
	Body   Object
}

// Hooks customize how the fake treats one collection, identified by its final path
// segment (e.g. "agents"), to mimic server-side behavior such as defaulting.
type Hooks struct {
	// KeyField names the request body field that identifies a created object, for
	// collections keyed by a natural key (such as members, keyed by user_id) rather
	// than by a generated ID.
	KeyField string
	// OnCreate adjusts a newly created object.
	OnCreate func(obj Object)
	// OnUpdate adjusts an object after an update body has been merged into it; before is
	// a copy of the object prior to the update. Returning an error rejects the update,
	// leaving the object unchanged.
	OnUpdate func(before, after, body Object) error
}

// Error is an error response a hook returns to reject a request.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Server is a fake Anthropic API server.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	mux      *http.ServeMux
	objects  map[string]Object
	requests []Request
	hooks    map[string]Hooks
	seq      int
}

// New starts a fake server that is shut down when the test ends.
func New(t *testing.T) *Server {
	s := &Server{
		mux:     http.NewServeMux(),
		objects: map[string]Object{},
		hooks:   map[string]Hooks{},
	}
	s.mux.HandleFunc("/", s.serveREST)
	s.Server = httptest.NewServer(http.HandlerFunc(s.record))
	t.Cleanup(s.Close)
	return s
}

// Handle registers a handler for an endpoint that departs from the REST conventions,
// using http.ServeMux patterns such as "POST /v1/tunnels/{id}/reveal_token".
func (s *Server) Handle(pattern string, handler func(w http.ResponseWriter, r *http.Request, body Object)) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		handler(w, r, s.lastBody())
	})
}

// SetHooks sets the hooks for the collection whose final path segment is name.
func (s *Server) SetHooks(name string, hooks Hooks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks[name] = hooks
}

// Requests returns the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// LastRequest returns the most recent request with the given method and path, failing
// the test if there is none.
func (s *Server) LastRequest(t *testing.T, method, path string) Request {
	t.Helper()
	reqs := s.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == method && reqs[i].Path == path {
			return reqs[i]
		}
	}
	t.Fatalf("no %s %s request was made", method, path)
	return Request{}
}

// Last returns the most recent request.
func (s *Server) Last() Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[len(s.requests)-1]
}

// Object returns a copy of the stored object at path, or nil.
func (s *Server) Object(path string) Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	if obj, ok := s.objects[path]; ok {
		return maps.Clone(obj)
	}
	return nil
}

// Put stores an object directly, for seeding objects the API cannot create (such as
// users) and for simulating changes made outside of Pulumi.
func (s *Server) Put(path string, obj Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[path] = obj
}

// Remove deletes a stored object, simulating a deletion made outside of Pulumi.
func (s *Server) Remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, path)
}

// Mutate changes a stored object in place, simulating a change made outside of Pulumi.
func (s *Server) Mutate(path string, f func(Object)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s.objects[path])
}

// NewID returns a fresh ID with the given prefix.
func (s *Server) NewID(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("%s_%04d", prefix, s.seq)
}

// record logs the request, then dispatches it.
func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	var body Object
	if r.Header.Get("Content-Type") == "application/json" {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				WriteError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
				return
			}
		}
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	s.mu.Unlock()
	s.mux.ServeHTTP(w, r)
}

func (s *Server) lastBody() Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[len(s.requests)-1].Body
}

// serveREST implements the uniform REST conventions.
func (s *Server) serveREST(w http.ResponseWriter, r *http.Request) {
	body := s.lastBody()
	path := strings.TrimSuffix(r.URL.Path, "/")

	s.mu.Lock()
	defer s.mu.Unlock()
	obj, exists := s.objects[path]
	parent, action := splitLast(path)
	switch {
	case r.Method == http.MethodGet && exists:
		WriteJSON(w, obj)
	case r.Method == http.MethodGet && !idLike.MatchString(path) &&
		(s.objects[parent] != nil || !idLike.MatchString(parent)):
		items := []any{}
		for _, p := range slices.Sorted(maps.Keys(s.objects)) {
			if itemParent, _ := splitLast(p); itemParent == path {
				items = append(items, s.objects[p])
			}
		}
		WriteJSON(w, Object{"data": items, "has_more": false})
	case r.Method == http.MethodDelete && exists:
		for p := range s.objects {
			if p == path || strings.HasPrefix(p, path+"/") {
				delete(s.objects, p)
			}
		}
		_, name := splitLast(parent)
		WriteJSON(w, Object{"type": singular(name) + "_deleted", "id": obj["id"]})
	case r.Method == http.MethodPost && exists:
		updated := deepClone(obj)
		merge(updated, deepClone(body))
		updated["updated_at"] = now()
		_, name := splitLast(parent)
		if h := s.hooks[name].OnUpdate; h != nil {
			if err := h(obj, updated, body); err != nil {
				var apiErr *Error
				if errors.As(err, &apiErr) {
					WriteError(w, apiErr.Status, apiErr.Message)
				} else {
					WriteError(w, http.StatusBadRequest, err.Error())
				}
				return
			}
		}
		s.objects[path] = updated
		WriteJSON(w, updated)
	case r.Method == http.MethodPost && action == "archive" && s.objects[parent] != nil:
		target := s.objects[parent]
		if target["archived_at"] == nil {
			target["archived_at"] = now()
		}
		WriteJSON(w, target)
	case r.Method == http.MethodPost:
		s.seq++
		created := deepClone(body)
		if created == nil {
			created = Object{}
		}
		created["id"] = fmt.Sprintf("%s_%04d", singular(action), s.seq)
		if key := s.hooks[action].KeyField; key != "" {
			created["id"] = body[key]
		}
		created["type"] = singular(action)
		created["created_at"] = now()
		created["updated_at"] = now()
		created["archived_at"] = nil
		if h := s.hooks[action].OnCreate; h != nil {
			h(created)
		}
		s.objects[path+"/"+created["id"].(string)] = created
		WriteJSON(w, created)
	default:
		WriteError(w, http.StatusNotFound, "not found: "+r.Method+" "+path)
	}
}

// merge applies a partial update: null clears a field, objects merge key by key,
// recursively, and anything else replaces the stored value. A real endpoint may replace
// an object wholesale instead; the provider's update bodies are correct under either.
func merge(obj, patch Object) {
	for k, v := range patch {
		existing, isMap := obj[k].(map[string]any)
		update, patchIsMap := v.(map[string]any)
		switch {
		case v == nil:
			delete(obj, k)
		case isMap && patchIsMap:
			merge(existing, update)
		default:
			obj[k] = v
		}
	}
}

// WriteJSON writes a 200 response with a JSON body.
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes an error response in the API's error shape.
func WriteError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Object{
		"type":  "error",
		"error": Object{"type": "error", "message": message},
	})
}

// deepClone copies an object through JSON, so nested maps are not shared.
func deepClone(obj Object) Object {
	raw, _ := json.Marshal(obj)
	var clone Object
	_ = json.Unmarshal(raw, &clone)
	return clone
}

// idLike matches a path whose last segment is an object ID, such as .../vaults/vault_0001.
// A GET of any other path lists a collection, which requires its parent object, if it
// has one, to exist.
var idLike = regexp.MustCompile(`_\d+$`)

func splitLast(path string) (string, string) {
	i := strings.LastIndex(path, "/")
	return path[:i], path[i+1:]
}

func singular(name string) string {
	return strings.TrimSuffix(name, "s")
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

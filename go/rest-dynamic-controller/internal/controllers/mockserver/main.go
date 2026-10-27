package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Resource struct {
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Status      string            `json:"status,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   string            `json:"createdAt,omitempty"`
	UpdatedAt   string            `json:"updatedAt,omitempty"`
}

type MockServer struct {
	resources map[string]*Resource
	pending   map[string]*Resource // Resources in pending state for async operations
	mutex     sync.RWMutex
	port      int

	// Test configuration flags
	simulateErrors  bool
	responseDelay   time.Duration
	authFailures    bool
	asyncOperations bool
	// lingerOnDelete makes DELETE answer 204 while KEEPING the resource, reproducing an API that
	// deletes asynchronously and declares no pollable operation: the 2xx means "deletion requested",
	// not "deletion completed", and a subsequent GET still returns the resource.
	lingerOnDelete bool
	// deleteErrorsButRemoves reproduces an API that REMOVES the resource and still answers the DELETE
	// with an error -- Aruba security/Kms answers 400 "Some kms keys are not deleted" for a key a
	// direct GET already 404s on (#101). The delete status code is a proxy; the observe verb is truth.
	deleteErrorsButRemoves bool

	// --- dependent lookups (#120) ---
	//
	// lookupCalls counts, per path, how many times each lookup endpoint was actually hit. It is the
	// GROUND TRUTH for the `when` guard: the only way to prove a guarded-off lookup did not run is to ask
	// the vendor whether it was called, because the controller reaching Ready proves nothing either way.
	lookupCalls map[string]int
	// lastRefBody is the RAW body the ref-create endpoint last received. Asserting on this, rather than on
	// the CR's status, is what shows the RESOLVED value actually reached the API.
	lastRefBody map[string]interface{}
	refs        map[string]map[string]interface{}
	// tombstoneOnDelete reproduces an API that answers GET for a deleted resource with 200 and a
	// record carrying status "deleted", rather than 404 (#111, Aruba security/Kmip).
	tombstoneOnDelete bool

	// --- async operations ---
	//
	// The RDC async engine had NO integration coverage before this: no testdata declared `async:`, so
	// driveAsync (inline poll) and driveAsyncRequeue (poll across reconciles) were exercised only by unit
	// tests over their helpers -- the same configuration that hid the whole dispatch verb being undriven.
	//
	// POST /async/resource stores the resource and answers 202 with an operation handle and NO resource
	// body, which is the shape the engine exists for: the create tells you an operation started, not what
	// it produced. GET /operations/{id} then answers "running" until it has been polled
	// asyncPollsBeforeSuccess times, and "succeeded" after -- so a test can prove the controller actually
	// polls rather than succeeding on the first look.
	operationPolls          map[string]int
	asyncPollsBeforeSuccess int
	asyncOpSeq              int

	// --- dispatch (fire-and-correlate) ---
	//
	// These reproduce GitHub Actions workflow_dispatch, which is the shape the dispatch verb was built
	// for and the one the shipped example documents:
	//
	//   POST /workflows/{id}/dispatches -> 204 with NO BODY and no identifier of any kind. The trigger
	//   tells you nothing about what it started, which is the entire reason correlation exists.
	//
	//   GET /workflows/{id}/runs -> {"workflow_runs": [...]}. GitHub does NOT echo the dispatch inputs
	//   onto the run, so a caller cannot find its own invocation by the token it sent. The token
	//   arrives instead in display_title, because the workflow sets run-name from the input.
	//
	// Getting that asymmetry wrong in either direction is what an integration case has to catch: a mock
	// that echoes the inputs would let a broken correlation implementation pass.
	runs []*WorkflowRun
	// dispatchCount counts triggers, so a test can prove the fire-once annotation is honoured rather
	// than merely that the CR looks right afterwards.
	dispatchCount int
	// swallowDispatch accepts the trigger and records NO run, reproducing a dispatch whose invocation
	// never becomes observable -- a workflow deleted, disabled, or filtered out by its own `on:`
	// conditions. This is what drives the observe-miss budget to DispatchedButNotObserved.
	swallowDispatch bool
	// echoTokenInOutputs puts the token somewhere a correlated lookup should NOT find it, so a test can
	// show the match is made on the configured echoedAt path and not by scanning the document.
	echoTokenInOutputs bool
}

// WorkflowRun mirrors the subset of GitHub's workflow-run object the dispatch verb reads.
type WorkflowRun struct {
	ID           int64  `json:"id"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion,omitempty"`
	DisplayTitle string `json:"display_title"`
	HTMLURL      string `json:"html_url,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	// Outputs is not a GitHub field. It exists only so echoTokenInOutputs can place the token off the
	// configured echoedAt path.
	Outputs map[string]string `json:"outputs,omitempty"`
}

func NewMockServer(port int) *MockServer {
	return &MockServer{
		resources:               make(map[string]*Resource),
		pending:                 make(map[string]*Resource),
		operationPolls:          make(map[string]int),
		asyncPollsBeforeSuccess: 2,
		port:                    port,
	}
}

func (ms *MockServer) authenticate(r *http.Request) bool {
	if ms.authFailures {
		return false
	}

	auth := r.Header.Get("Authorization")
	return strings.HasPrefix(auth, "Bearer test") || auth == "Bearer test"
}

func (ms *MockServer) delay() {
	if ms.responseDelay > 0 {
		time.Sleep(ms.responseDelay)
	}
}

func (ms *MockServer) writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (ms *MockServer) writeJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}

// GET /resource - Lista risorse con parametri di query opzionali
func (ms *MockServer) listResources(w http.ResponseWriter, r *http.Request) {
	ms.delay()

	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if ms.simulateErrors {
		ms.writeError(w, http.StatusInternalServerError, "simulated server error")
		return
	}

	ms.mutex.RLock()
	defer ms.mutex.RUnlock()

	// Supporta filtri per nome (come usato nei test)
	nameFilter := r.URL.Query().Get("name")

	var result []*Resource
	for _, resource := range ms.resources {
		if nameFilter == "" || resource.Name == nameFilter {
			result = append(result, resource)
		}
	}

	// Se non trova nessuna risorsa con quel nome, restituisce 404
	if nameFilter != "" && len(result) == 0 {
		ms.writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	// Formato lista
	ms.writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": result,
		"count": len(result),
	})
}

// GET /resource/{id} - Ottiene risorsa specifica per ID
func (ms *MockServer) getResource(w http.ResponseWriter, r *http.Request) {
	ms.delay()

	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if ms.simulateErrors {
		ms.writeError(w, http.StatusInternalServerError, "simulated server error")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/resource/")
	if id == "" {
		ms.writeError(w, http.StatusBadRequest, "missing resource id")
		return
	}

	ms.mutex.RLock()
	fmt.Println("Looking for resource with ID:", id)
	resource, exists := ms.resources[id]
	if !exists {
		fmt.Println("Resource not found in resources map, checking pending", id)
		resource, exists = ms.pending[id]
		fmt.Println("Pending resource found:", exists, "for ID:", id)
	}
	ms.mutex.RUnlock()

	if !exists {
		ms.writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	ms.writeJSON(w, http.StatusOK, resource)
}

// POST /resource - Crea nuova risorsa
func (ms *MockServer) createResource(w http.ResponseWriter, r *http.Request) {
	ms.delay()

	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if ms.simulateErrors {
		ms.writeError(w, http.StatusInternalServerError, "simulated server error")
		return
	}

	var resource Resource
	if err := json.NewDecoder(r.Body).Decode(&resource); err != nil {
		ms.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if resource.Name == "" {
		ms.writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	// Genera ID se non fornito
	if resource.ID == "" {
		resource.ID = resource.Name // Usa il nome come ID per i test
	}

	// Controlla se la risorsa esiste già
	if _, exists := ms.resources[resource.ID]; exists {
		ms.writeError(w, http.StatusConflict, "resource already exists")
		return
	}

	// Imposta timestamps e stato di default
	now := time.Now().UTC().Format(time.RFC3339)
	resource.CreatedAt = now
	resource.UpdatedAt = now
	if resource.Status == "" {
		resource.Status = "created"
	}

	// Supporta operazioni asincrone (202 Accepted)
	if ms.asyncOperations {
		ms.pending[resource.ID] = &resource // <-- store in pending
		ms.writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"operationId": fmt.Sprintf("op-%d", time.Now().Unix()),
			"status":      "pending",
			"resource":    resource,
		})
		return
	}

	ms.resources[resource.ID] = &resource
	ms.writeJSON(w, http.StatusCreated, resource)
}

// PUT /resource/{id} - Aggiorna risorsa esistente
func (ms *MockServer) updateResource(w http.ResponseWriter, r *http.Request) {
	ms.delay()

	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if ms.simulateErrors {
		ms.writeError(w, http.StatusInternalServerError, "simulated server error")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/resource/")
	if id == "" {
		ms.writeError(w, http.StatusBadRequest, "missing resource id")
		return
	}

	var updateData Resource
	if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
		ms.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	resource, exists := ms.resources[id]
	if !exists {
		ms.writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	// Aggiorna campi
	if updateData.Name != "" {
		resource.Name = updateData.Name
	}
	if updateData.Description != "" {
		resource.Description = updateData.Description
	}
	if updateData.Status != "" {
		resource.Status = updateData.Status
	}
	if updateData.Metadata != nil {
		if resource.Metadata == nil {
			resource.Metadata = make(map[string]string)
		}
		for k, v := range updateData.Metadata {
			resource.Metadata[k] = v
		}
	}

	resource.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	// Supporta operazioni asincrone
	if ms.asyncOperations {
		ms.writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"operationId": fmt.Sprintf("op-update-%d", time.Now().Unix()),
			"status":      "pending",
			"resource":    resource,
		})
		return
	}

	ms.writeJSON(w, http.StatusOK, resource)
}

// PATCH /resource/{id} - Aggiorna parzialmente risorsa
func (ms *MockServer) patchResource(w http.ResponseWriter, r *http.Request) {
	ms.delay()

	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if ms.simulateErrors {
		ms.writeError(w, http.StatusInternalServerError, "simulated server error")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/resource/")
	if id == "" {
		ms.writeError(w, http.StatusBadRequest, "missing resource id")
		return
	}

	var patchData map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&patchData); err != nil {
		ms.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	resource, exists := ms.resources[id]
	if !exists {
		ms.writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	// Applica patch
	if name, ok := patchData["name"].(string); ok {
		resource.Name = name
	}
	if description, ok := patchData["description"].(string); ok {
		resource.Description = description
	}
	if status, ok := patchData["status"].(string); ok {
		resource.Status = status
	}

	resource.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	ms.writeJSON(w, http.StatusOK, resource)
}

// DELETE /resource/{id} - Elimina risorsa
func (ms *MockServer) deleteResource(w http.ResponseWriter, r *http.Request) {
	ms.delay()

	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if ms.simulateErrors {
		ms.writeError(w, http.StatusInternalServerError, "simulated server error")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/resource/")
	if id == "" {
		ms.writeError(w, http.StatusBadRequest, "missing resource id")
		return
	}

	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	if _, exists := ms.resources[id]; !exists {
		ms.writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	// tombstoneOnDelete: mark the record deleted but KEEP serving it on GET with 200, which is what an
	// API with soft deletes does. The resource is gone as far as the user is concerned.
	if ms.tombstoneOnDelete {
		if r, ok := ms.resources[id]; ok {
			r.Status = "deleted"
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// deleteErrorsButRemoves: actually remove the resource, then answer with an error anyway. A
	// following GET 404s, so the observe verb disagrees with the delete status code.
	if ms.deleteErrorsButRemoves {
		delete(ms.resources, id)
		ms.writeError(w, http.StatusBadRequest, "Some items are not deleted")
		return
	}

	// lingerOnDelete: report success WITHOUT removing the resource, so a following GET still finds it.
	// This is what an asynchronously-deleting API does when its OAS declares no pollable operation.
	if ms.lingerOnDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	delete(ms.resources, id)

	// Supporta operazioni asincrone
	if ms.asyncOperations {
		ms.writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"operationId": fmt.Sprintf("op-delete-%d", time.Now().Unix()),
			"status":      "pending",
		})
		return
	}

	// Restituisce 204 No Content per eliminazione riuscita
	w.WriteHeader(http.StatusNoContent)
}

// GET /health - Health check endpoint
func (ms *MockServer) healthCheck(w http.ResponseWriter, r *http.Request) {
	ms.writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":           "healthy",
		"time":             time.Now().UTC().Format(time.RFC3339),
		"resources_count":  len(ms.resources),
		"simulate_errors":  ms.simulateErrors,
		"auth_failures":    ms.authFailures,
		"async_operations": ms.asyncOperations,
		"response_delay":   ms.responseDelay.String(),
	})
}

// POST /async/resource - create a resource asynchronously.
//
// Answers 202 with {"operationId": "..."} and NO resource body. The resource IS stored, so a postGet or
// a status re-read can find it once the operation reports success.
func (ms *MockServer) asyncCreateResource(w http.ResponseWriter, r *http.Request) {
	// Authenticated like every other resource endpoint. Load-bearing for what this fixture is FOR: the
	// async paths resolve their own client now (#220), and a client handed out without SetAuth would go
	// out unauthenticated. An endpoint that skipped this check could not tell that apart from a correct
	// one -- an assertion that cannot fail.
	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var resource Resource
	if err := json.NewDecoder(r.Body).Decode(&resource); err != nil {
		ms.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if resource.Name == "" {
		ms.writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	ms.mutex.Lock()
	if resource.ID == "" {
		resource.ID = resource.Name
	}
	resource.Status = "active"
	resource.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	ms.resources[resource.ID] = &resource
	ms.asyncOpSeq++
	opID := fmt.Sprintf("op-%d", ms.asyncOpSeq)
	ms.operationPolls[opID] = 0
	ms.mutex.Unlock()

	log.Printf("ASYNC create: id=%s operation=%s (202, no resource body)", resource.ID, opID)
	w.Header().Set("Content-Type", "application/json")
	// Operation-Location as well as the body handle, so a definition can read the handle from either.
	w.Header().Set("Operation-Location", "http://localhost:30007/operations/"+opID)
	w.WriteHeader(http.StatusAccepted)
	// The handle AND the resource's id. Real async APIs do this -- the create knows what it allocated even
	// though it has not finished provisioning it -- and it is what makes a postGet keyed by a
	// server-assigned id possible at all: the id is projected into status from this response (#229).
	//
	// An API that returned the handle ALONE, with the id nowhere until the resource is fetched, cannot be
	// re-read by id by any mechanism: you would need the id to fetch the thing that would tell you the id.
	// That shape needs findby, which matches on identifiers the CR already has.
	_ = json.NewEncoder(w).Encode(map[string]string{"operationId": opID, "id": resource.ID})
}

// GET /operations/{id} - report an operation's status, "running" until it has been polled enough times.
func (ms *MockServer) operationStatus(w http.ResponseWriter, r *http.Request) {
	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/operations/")

	ms.mutex.Lock()
	seen, known := ms.operationPolls[id]
	if !known {
		ms.mutex.Unlock()
		ms.writeError(w, http.StatusNotFound, "no such operation")
		return
	}
	seen++
	ms.operationPolls[id] = seen
	status := "running"
	if seen > ms.asyncPollsBeforeSuccess {
		status = "succeeded"
	}
	ms.mutex.Unlock()

	log.Printf("ASYNC poll: operation=%s poll=%d status=%s", id, seen, status)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"operationId": id, "status": status})
}

// GET /admin/operation-polls/{id} - how many times the VENDOR was polled for this operation. A test
// asserting the controller polled cannot learn that from the CR.
func (ms *MockServer) operationPollCount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/admin/operation-polls/")
	ms.mutex.RLock()
	n := ms.operationPolls[id]
	ms.mutex.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"polls": n})
}

// POST /workflows/{id}/dispatches - trigger a workflow run.
//
// Answers 204 with an EMPTY body, exactly as GitHub does. Nothing in the response identifies the run
// that was started; the correlation token the caller put in the request body is not echoed back and is
// not attached to the run. The workflow itself is what surfaces it, via run-name -> display_title.
func (ms *MockServer) dispatchWorkflow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref    string                 `json:"ref"`
		Inputs map[string]interface{} `json:"inputs"`
	}
	// Read the body by LENGTH OF WHAT ARRIVED, never by r.ContentLength. The controller sends this
	// request chunked -- measured: content-length=-1, transfer-encoding=[chunked] -- so a guard of
	// `if r.ContentLength > 0` is false for every single dispatch and silently skips the decode. That
	// cost an afternoon: the body was always correct and this fixture dropped it, which presented as
	// "the request never carries the correlation token" and was very nearly filed as a product bug.
	rawBody, _ := io.ReadAll(r.Body)
	// Log the RAW bytes, not the decoded struct: decoding first makes an unexpected shape and an empty
	// body look identical, which is what hid the guard above from me in the first place.
	log.Printf("DISPATCH received: path=%s content-type=%q content-length=%d transfer-encoding=%v raw=%s",
		r.URL.Path, r.Header.Get("Content-Type"), r.ContentLength, r.TransferEncoding, string(rawBody))

	// A malformed body is a client error; an EMPTY one is not -- ref/inputs are both optional here.
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &body); err != nil {
			ms.writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	ms.mutex.Lock()
	ms.dispatchCount++
	if !ms.swallowDispatch {
		// The token reaches the run ONLY through display_title. Read it from the inputs the caller sent
		// to simulate the workflow's own `run-name: ${{ inputs.correlationId }}`.
		title := ""
		if body.Inputs != nil {
			if v, ok := body.Inputs["correlationId"]; ok {
				title = fmt.Sprintf("%v", v)
			}
		}
		run := &WorkflowRun{
			ID:           int64(100 + ms.dispatchCount),
			Status:       "completed",
			Conclusion:   "success",
			DisplayTitle: title,
			HTMLURL:      fmt.Sprintf("https://example.invalid/runs/%d", 100+ms.dispatchCount),
			CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		}
		if ms.echoTokenInOutputs {
			// Token present in the document but NOT at echoedAt: a correlated lookup must miss it.
			run.DisplayTitle = "some unrelated run name"
			run.Outputs = map[string]string{"correlationId": title}
		}
		log.Printf("DISPATCH recorded run: id=%d display_title=%q", run.ID, run.DisplayTitle)
		ms.runs = append(ms.runs, run)
	}
	ms.mutex.Unlock()

	// 204, no body. Deliberately no Content-Type and nothing written.
	w.WriteHeader(http.StatusNoContent)
}

// GET /workflows/{id}/runs - list runs, wrapped as GitHub wraps them.
func (ms *MockServer) listWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	ms.mutex.RLock()
	out := make([]*WorkflowRun, len(ms.runs))
	copy(out, ms.runs)
	ms.mutex.RUnlock()

	titles := make([]string, 0, len(out))
	for _, r := range out {
		titles = append(titles, r.DisplayTitle)
	}
	log.Printf("RUNS listed: path=%s count=%d display_titles=%v", r.URL.Path, len(out), titles)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"total_count":   len(out),
		"workflow_runs": out,
	})
}

// GET /admin/dispatch-count - how many triggers the server has accepted, so a test can assert
// fire-once from the SERVER's side rather than from the CR's annotations.
func (ms *MockServer) dispatchCountHandler(w http.ResponseWriter, r *http.Request) {
	ms.mutex.RLock()
	n := ms.dispatchCount
	ms.mutex.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"dispatchCount": n})
}

// POST /admin/config - Configura comportamento del server per i test
func (ms *MockServer) configureServer(w http.ResponseWriter, r *http.Request) {
	var config struct {
		SimulateErrors          *bool `json:"simulateErrors,omitempty"`
		ResponseDelay           *int  `json:"responseDelayMs,omitempty"`
		AuthFailures            *bool `json:"authFailures,omitempty"`
		AsyncOperations         *bool `json:"asyncOperations,omitempty"`
		CompletePendingAsync    *bool `json:"completePendingAsync,omitempty"`
		LingerOnDelete          *bool `json:"lingerOnDelete,omitempty"`
		DeleteErrorsButRemoves  *bool `json:"deleteErrorsButRemoves,omitempty"`
		TombstoneOnDelete       *bool `json:"tombstoneOnDelete,omitempty"`
		SwallowDispatch         *bool `json:"swallowDispatch,omitempty"`
		EchoTokenInOutputs      *bool `json:"echoTokenInOutputs,omitempty"`
		ResetDispatch           *bool `json:"resetDispatch,omitempty"`
		AsyncPollsBeforeSuccess *int  `json:"asyncPollsBeforeSuccess,omitempty"`
		ResetAsyncOperations    *bool `json:"resetAsyncOperations,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		ms.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	b, _ := json.MarshalIndent(config, "", "  ")
	fmt.Println("Received configuration:", string(b))

	if config.CompletePendingAsync != nil && *config.CompletePendingAsync {
		ms.mutex.Lock()
		for id, res := range ms.pending {
			fmt.Println("Completing pending async operation for resource:", id)
			ms.resources[id] = res

			b, _ := json.MarshalIndent(ms.resources, "", "  ")
			fmt.Println("Current resources after completing pending:", string(b))

		}
		ms.pending = make(map[string]*Resource)
		ms.mutex.Unlock()
	}
	if config.AsyncPollsBeforeSuccess != nil {
		ms.asyncPollsBeforeSuccess = *config.AsyncPollsBeforeSuccess
	}
	if config.ResetAsyncOperations != nil && *config.ResetAsyncOperations {
		ms.mutex.Lock()
		ms.operationPolls = make(map[string]int)
		ms.asyncOpSeq = 0
		ms.mutex.Unlock()
	}
	if config.SwallowDispatch != nil {
		ms.swallowDispatch = *config.SwallowDispatch
	}
	if config.EchoTokenInOutputs != nil {
		ms.echoTokenInOutputs = *config.EchoTokenInOutputs
	}
	if config.ResetDispatch != nil && *config.ResetDispatch {
		ms.mutex.Lock()
		ms.runs = nil
		ms.dispatchCount = 0
		ms.mutex.Unlock()
	}
	if config.LingerOnDelete != nil {
		ms.lingerOnDelete = *config.LingerOnDelete
	}
	if config.DeleteErrorsButRemoves != nil {
		ms.deleteErrorsButRemoves = *config.DeleteErrorsButRemoves
	}
	if config.TombstoneOnDelete != nil {
		ms.tombstoneOnDelete = *config.TombstoneOnDelete
	}
	if config.SimulateErrors != nil {
		ms.simulateErrors = *config.SimulateErrors
	}
	if config.ResponseDelay != nil {
		ms.responseDelay = time.Duration(*config.ResponseDelay) * time.Millisecond
	}
	if config.AuthFailures != nil {
		ms.authFailures = *config.AuthFailures
	}
	if config.AsyncOperations != nil {
		ms.asyncOperations = *config.AsyncOperations
	}

	ms.writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "configuration updated",
		"config": map[string]interface{}{
			"simulateErrors":  ms.simulateErrors,
			"responseDelay":   ms.responseDelay.String(),
			"authFailures":    ms.authFailures,
			"asyncOperations": ms.asyncOperations,
		},
	})
}

// GET /status/{code} - Restituisce codice di stato specifico per test
func (ms *MockServer) statusCode(w http.ResponseWriter, r *http.Request) {
	codeStr := strings.TrimPrefix(r.URL.Path, "/status/")
	code, err := strconv.Atoi(codeStr)
	if err != nil {
		ms.writeError(w, http.StatusBadRequest, "invalid status code")
		return
	}

	switch code {
	case http.StatusNoContent, http.StatusNotModified:
		w.WriteHeader(code)
		return
	case http.StatusAccepted:
		ms.writeJSON(w, code, map[string]interface{}{
			"status":  "pending",
			"message": "request accepted for processing",
		})
		return
	default:
		w.WriteHeader(code)
		if code >= 400 {
			ms.writeJSON(w, code, map[string]interface{}{
				"error": fmt.Sprintf("simulated error with status %d", code),
			})
		} else {
			ms.writeJSON(w, code, map[string]interface{}{
				"status": fmt.Sprintf("simulated response with status %d", code),
			})
		}
	}
}

func (ms *MockServer) router(w http.ResponseWriter, r *http.Request) {
	// Abilita CORS per i test
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch {
	case r.URL.Path == "/health":
		ms.healthCheck(w, r)
	case r.URL.Path == "/admin/config" && r.Method == http.MethodPost:
		ms.configureServer(w, r)
	case r.URL.Path == "/async/resource" && r.Method == http.MethodPost:
		ms.asyncCreateResource(w, r)
	case strings.HasPrefix(r.URL.Path, "/operations/") && r.Method == http.MethodGet:
		ms.operationStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/operation-polls/") && r.Method == http.MethodGet:
		ms.operationPollCount(w, r)
	case r.URL.Path == "/admin/dispatch-count" && r.Method == http.MethodGet:
		ms.dispatchCountHandler(w, r)
	case r.URL.Path == "/admin/lookup-calls" && r.Method == http.MethodGet:
		ms.lookupCallsHandler(w, r)
	case strings.HasPrefix(r.URL.Path, "/lookup/projects/") && r.Method == http.MethodGet:
		ms.lookupProject(w, r)
	case strings.HasPrefix(r.URL.Path, "/lookup/profiles/") && r.Method == http.MethodGet:
		ms.lookupProfile(w, r)
	case r.URL.Path == "/lookup/flows" && r.Method == http.MethodGet:
		ms.lookupFlows(w, r)
	case r.URL.Path == "/lookup/refs" && r.Method == http.MethodPost:
		ms.createRef(w, r)
	case strings.HasPrefix(r.URL.Path, "/lookup/refs/"):
		switch r.Method {
		case http.MethodGet:
			ms.getRef(w, r)
		case http.MethodDelete:
			ms.deleteRef(w, r)
		default:
			ms.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case strings.HasPrefix(r.URL.Path, "/status/"):
		ms.statusCode(w, r)
	case strings.HasPrefix(r.URL.Path, "/workflows/") && strings.HasSuffix(r.URL.Path, "/dispatches") && r.Method == http.MethodPost:
		ms.dispatchWorkflow(w, r)
	case strings.HasPrefix(r.URL.Path, "/workflows/") && strings.HasSuffix(r.URL.Path, "/runs") && r.Method == http.MethodGet:
		ms.listWorkflowRuns(w, r)
	// POST /v2/resource exists so a two-document RestDefinition can have a create verb whose operation is
	// ABSENT from the primary document, which is the shape per-verb oasPath exists for (#220): Aruba
	// CloudServer creates against compute-provider 1.1, whose endpoint the 1.0 document does not declare.
	// Behaves exactly like POST /resource; only its path differs.
	case r.URL.Path == "/v2/resource" && r.Method == http.MethodPost:
		ms.createResource(w, r)
	case r.URL.Path == "/resource":
		switch r.Method {
		case http.MethodGet:
			ms.listResources(w, r)
		case http.MethodPost:
			ms.createResource(w, r)
		default:
			ms.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case strings.HasPrefix(r.URL.Path, "/resource/"):
		switch r.Method {
		case http.MethodGet:
			ms.getResource(w, r)
		case http.MethodPut:
			ms.updateResource(w, r)
		case http.MethodPatch:
			ms.patchResource(w, r)
		case http.MethodDelete:
			ms.deleteResource(w, r)
		default:
			ms.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		ms.writeError(w, http.StatusNotFound, "endpoint not found")
	}
}

// --- dependent lookups (#120) -------------------------------------------------------------------------
//
// Two shapes, because two consumers define the feature: a CHAIN (project -> its default profile -> that
// profile's token), which is GitRef's shape, and a MATCH over a list (the flow whose alias equals a CR
// field), which is KeycloakClient's.
//
// Every one of these endpoints AUTHENTICATES. That is deliberate and load-bearing: the lookup client is
// built separately from the verb client, and if it were handed out without SetAuth the request would go
// out unauthenticated. Against an endpoint that does not check, that passes silently -- which is how an
// earlier async fixture nearly hid exactly this bug.

func (ms *MockServer) noteLookupCall(path string) {
	ms.mutex.Lock()
	defer ms.mutex.Unlock()
	if ms.lookupCalls == nil {
		ms.lookupCalls = map[string]int{}
	}
	ms.lookupCalls[path]++
}

// GET /lookup/projects/{id} - first link of the chain: the project's default profile NAME.
func (ms *MockServer) lookupProject(w http.ResponseWriter, r *http.Request) {
	ms.delay()
	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ms.noteLookupCall("/lookup/projects")

	id := strings.TrimPrefix(r.URL.Path, "/lookup/projects/")
	if id == "" {
		ms.writeError(w, http.StatusNotFound, "project not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":             id,
		"defaultProfile": "main",
	})
}

// GET /lookup/profiles/{name} - second link: the token of the profile the first link named.
//
// The {name} it is addressed by is "profiles/main" rather than "main", mirroring GitHub's single {ref}
// parameter whose value must be formatted "heads/<branch>". That is what makes the chained value need a
// valueMapping, and testing the easy shape instead would miss the transform entirely.
func (ms *MockServer) lookupProfile(w http.ResponseWriter, r *http.Request) {
	ms.delay()
	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ms.noteLookupCall("/lookup/profiles")

	name := strings.TrimPrefix(r.URL.Path, "/lookup/profiles/")
	if name != "profiles/main" {
		ms.writeError(w, http.StatusNotFound, "profile not found: "+name)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"name":  name,
		"token": map[string]interface{}{"value": "tok-abc123"},
	})
}

// GET /lookup/flows - a BARE ARRAY, which is why LookupMatch.ItemsAt defaults to ".".
//
// It deliberately contains a near-miss alias ("browser" alongside "browser-mfa") so a match that compared
// by prefix, or scoped server-side and took items[0], would bind the wrong id and the test would catch it.
func (ms *MockServer) lookupFlows(w http.ResponseWriter, r *http.Request) {
	ms.delay()
	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ms.noteLookupCall("/lookup/flows")

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode([]map[string]interface{}{
		{"alias": "browser", "id": "flow-browser"},
		{"alias": "browser-mfa", "id": "flow-browser-mfa"},
		{"alias": "direct-grant", "id": "flow-direct-grant"},
	})
}

// POST /lookup/refs - records the RAW body so a test can assert what the API actually received.
func (ms *MockServer) createRef(w http.ResponseWriter, r *http.Request) {
	ms.delay()
	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		ms.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	name, _ := body["name"].(string)
	if name == "" {
		ms.writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	ms.mutex.Lock()
	if ms.refs == nil {
		ms.refs = map[string]map[string]interface{}{}
	}
	stored := map[string]interface{}{"id": name}
	for k, v := range body {
		stored[k] = v
	}
	ms.refs[name] = stored
	ms.lastRefBody = body
	ms.mutex.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(stored)
}

// GET /lookup/refs/{id}
func (ms *MockServer) getRef(w http.ResponseWriter, r *http.Request) {
	ms.delay()
	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/lookup/refs/")

	ms.mutex.RLock()
	stored, ok := ms.refs[id]
	ms.mutex.RUnlock()
	if !ok {
		ms.writeError(w, http.StatusNotFound, "ref not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stored)
}

// DELETE /lookup/refs/{id}
func (ms *MockServer) deleteRef(w http.ResponseWriter, r *http.Request) {
	ms.delay()
	if !ms.authenticate(r) {
		ms.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/lookup/refs/")

	ms.mutex.Lock()
	_, ok := ms.refs[id]
	delete(ms.refs, id)
	ms.mutex.Unlock()
	if !ok {
		ms.writeError(w, http.StatusNotFound, "ref not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /admin/lookup-calls - per-path call counts plus the last ref-create body.
func (ms *MockServer) lookupCallsHandler(w http.ResponseWriter, r *http.Request) {
	ms.mutex.RLock()
	calls := map[string]int{}
	for k, v := range ms.lookupCalls {
		calls[k] = v
	}
	last := ms.lastRefBody
	ms.mutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"calls":       calls,
		"lastRefBody": last,
	})
}

func (ms *MockServer) Start() error {
	// Pre-popola con dati di test che corrispondono a quelli attesi nei test
	ms.mutex.Lock()
	ms.mutex.Unlock()

	http.HandleFunc("/", ms.router)

	addr := fmt.Sprintf(":%d", ms.port)
	log.Printf("Mock server starting on %s", addr)
	log.Printf("Endpoints available:")
	log.Printf("  GET    /health")
	log.Printf("  POST   /admin/config (configure server behavior)")
	log.Printf("  GET    /resource (list/search resources)")
	log.Printf("  POST   /resource (create resource)")
	log.Printf("  GET    /resource/{id}")
	log.Printf("  PUT    /resource/{id}")
	log.Printf("  PATCH  /resource/{id}")
	log.Printf("  DELETE /resource/{id}")
	log.Printf("  GET    /status/{code} (return specific status code)")
	log.Printf("  POST   /workflows/{id}/dispatches (204, no body -- GitHub workflow_dispatch)")
	log.Printf("  GET    /workflows/{id}/runs (list runs; token arrives in display_title)")
	log.Printf("  GET    /admin/dispatch-count")
	log.Printf("  POST   /async/resource (202 + operation handle, no body)")
	log.Printf("  GET    /operations/{id} (running until polled enough, then succeeded)")
	log.Printf("  POST   /v2/resource (create; declared only in the SECOND OAS document)")

	return http.ListenAndServe(addr, nil)
}

func main() {
	port := 30007
	server := NewMockServer(port)

	log.Fatal(server.Start())
}

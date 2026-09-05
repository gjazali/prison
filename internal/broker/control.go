package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/control"
	"prison/internal/broker/relay"
	"prison/internal/state"
	"prison/internal/vault"
)

// maxControlBodyBytes is the size limit for a control request body.
const maxControlBodyBytes = 4 << 20

// controlHandler builds the HTTP mux for the control socket endpoints.
// It returns an http.Handler.
func (broker *Broker) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", broker.handleStatus)
	mux.HandleFunc("POST /listen", broker.handleListen)
	mux.HandleFunc("PUT /projects/{id}", broker.handleUpdateProject)
	mux.HandleFunc("GET /projects/{id}/environment", broker.handleProjectEnvironment)
	mux.HandleFunc("POST /vault/unlock", broker.handleUnlock)
	mux.HandleFunc("POST /reload", broker.handleReload)
	mux.HandleFunc("GET /log", broker.handleLog)
	mux.HandleFunc("POST /shutdown", broker.handleShutdown)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		relay.WriteError(w, http.StatusNotFound,
			fmt.Sprintf("the broker has no %s %s endpoint", r.Method, r.URL.Path))
	})
	return mux
}

// writeJSON writes value as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		relay.WriteError(w, http.StatusInternalServerError,
			"cannot encode the answer: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// decodeJSONBody reads the request body as JSON into out. It returns
// false and writes a 400 response if decoding fails.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, out any) bool {
	body := http.MaxBytesReader(w, r.Body, maxControlBodyBytes)
	if err := json.NewDecoder(body).Decode(out); err != nil {
		relay.WriteError(w, http.StatusBadRequest,
			"the request body is not the JSON prison expects: "+err.Error())
		return false
	}
	return true
}

// handleStatus serves GET /status.
func (broker *Broker) handleStatus(w http.ResponseWriter, r *http.Request) {
	table := broker.projects.Load()
	projects := append([]string{}, table.ids...)
	writeJSON(w, http.StatusOK, control.Status{
		Version:             broker.options.Version,
		StateRoot:           broker.root.Path,
		PID:                 os.Getpid(),
		Listeners:           broker.listenerStates(),
		Vault:               broker.vaultState(),
		Projects:            projects,
		CredentialVariables: broker.credentialVariables(),
	})
}

// handleListen serves POST /listen. It starts binding on the requested
// address and reports the result after the first attempt.
func (broker *Broker) handleListen(w http.ResponseWriter, r *http.Request) {
	var request control.ListenRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	if request.Address == "" {
		relay.WriteError(w, http.StatusBadRequest,
			"an address to listen on is required")
		return
	}
	writeJSON(w, http.StatusOK, broker.listen(request.Address))
}

// handleUpdateProject serves PUT /projects/{id}. It saves the profile
// if provided, merges credentials, and reloads the project.
func (broker *Broker) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !state.IsProjectID(id) {
		relay.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("%q is not a project id; ids are twelve hex characters", id))
		return
	}
	project, err := broker.root.ProjectByID(id)
	if err != nil {
		relay.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	var update control.ProjectUpdate
	if !decodeJSONBody(w, r, &update) {
		return
	}
	if update.Profile != nil {
		if err := project.SaveProfile(update.Profile); err != nil {
			relay.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	broker.mergeCredentials(update.Credentials)
	if err := broker.reloadProject(id); err != nil {
		relay.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleProjectEnvironment serves GET /projects/{id}/environment.
func (broker *Broker) handleProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	snapshot := broker.snapshot(id)
	if snapshot == nil {
		relay.WriteError(w, http.StatusNotFound,
			fmt.Sprintf("no project %s is known to the broker; `prison up` registers one", id))
		return
	}
	lines := broker.environmentLines(snapshot)
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, control.Environment{Variables: lines})
}

// handleUnlock serves POST /vault/unlock. It returns 400 for a wrong
// passphrase, 404 if no vault exists, or 500 for other failures.
func (broker *Broker) handleUnlock(w http.ResponseWriter, r *http.Request) {
	var request control.UnlockRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	if request.Passphrase == "" {
		relay.WriteError(w, http.StatusBadRequest, "a passphrase is required")
		return
	}
	if err := broker.unlockVault(request.Passphrase); err != nil {
		status := http.StatusInternalServerError
		switch {
		case isWrongPassphrase(err):
			status = http.StatusBadRequest
		case !vault.Exists(broker.root.VaultFile()):
			status = http.StatusNotFound
		}
		relay.WriteError(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleReload serves POST /reload. It re-reads the floor and every
// project.
func (broker *Broker) handleReload(w http.ResponseWriter, r *http.Request) {
	broker.reloadFloor()
	if err := broker.reloadAll(); err != nil {
		relay.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLog serves GET /log. It returns matching entries, oldest first.
func (broker *Broker) handleLog(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := brokerlog.Filter{
		Kind:    query.Get("kind"),
		Project: query.Get("project"),
		Secret:  query.Get("secret"),
		Inmate:  query.Get("inmate"),
	}
	if limitText := query.Get("limit"); limitText != "" {
		limit, err := strconv.Atoi(limitText)
		if err != nil || limit < 0 {
			relay.WriteError(w, http.StatusBadRequest,
				fmt.Sprintf("%q is not a limit; give a whole number", limitText))
			return
		}
		filter.Limit = limit
	}
	entries, err := brokerlog.Read(broker.root.BrokerLog(), filter)
	if err != nil {
		relay.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []brokerlog.Entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleShutdown serves POST /shutdown and signals Run to return.
func (broker *Broker) handleShutdown(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	broker.requestShutdown()
}

// errNoProject is returned when a project disappears between tunnel
// setup and request handling.
var errNoProject = errors.New("this project is no longer known to prison")

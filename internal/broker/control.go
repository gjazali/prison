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

const maxControlBodyBytes = 4 << 20

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
	mux.HandleFunc("PUT /publish/{box}", broker.handlePublish)
	mux.HandleFunc("DELETE /publish/{box}", broker.handleUnpublish)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		relay.WriteError(w, http.StatusNotFound,
			fmt.Sprintf("no %s %s endpoint", r.Method, r.URL.Path))
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		relay.WriteError(w, http.StatusInternalServerError,
			"cannot encode the response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, out any) bool {
	body := http.MaxBytesReader(w, r.Body, maxControlBodyBytes)
	if err := json.NewDecoder(body).Decode(out); err != nil {
		relay.WriteError(w, http.StatusBadRequest,
			"the request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}

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

// handleListen reports the result of the first bind attempt.
func (broker *Broker) handleListen(w http.ResponseWriter, r *http.Request) {
	var request control.ListenRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	if request.Address == "" {
		relay.WriteError(w, http.StatusBadRequest, "an address is required")
		return
	}
	writeJSON(w, http.StatusOK, broker.listen(request.Address))
}

func (broker *Broker) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !state.IsProjectID(id) {
		relay.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("%q is not a project id", id))
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

func (broker *Broker) handleProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	snapshot := broker.snapshot(id)
	if snapshot == nil {
		relay.WriteError(w, http.StatusNotFound,
			fmt.Sprintf("unknown project %s. Run `prison up`", id))
		return
	}
	lines := broker.environmentLines(snapshot)
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, control.Environment{Variables: lines})
}

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

func (broker *Broker) handleReload(w http.ResponseWriter, r *http.Request) {
	broker.reloadFloor()
	if err := broker.reloadAll(); err != nil {
		relay.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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
				fmt.Sprintf("limit %q is not a whole number", limitText))
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

func (broker *Broker) handlePublish(w http.ResponseWriter, r *http.Request) {
	var request control.PublishRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		relay.WriteError(w, http.StatusBadRequest,
			"the publish request is not valid JSON")
		return
	}
	if err := broker.forwards.publish(r.PathValue("box"), request); err != nil {
		relay.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (broker *Broker) handleUnpublish(w http.ResponseWriter, r *http.Request) {
	broker.forwards.unpublish(r.PathValue("box"))
	w.WriteHeader(http.StatusNoContent)
}

func (broker *Broker) handleShutdown(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	broker.requestShutdown()
}

// errNoProject means that the project disappeared after tunnel setup.
var errNoProject = errors.New("unknown project")

package broker

import (
	"crypto/subtle"
	"fmt"
	"os"
	"sort"
	"time"

	"prison/internal/policy"
	"prison/internal/state"
)

// projectSnapshot holds a project's loaded state. It is read-only
// once built. A new snapshot replaces it on file changes.
type projectSnapshot struct {
	id            string
	token         string
	grants        []string
	granted       map[string]bool
	profile       *state.Profile
	profileEgress []policy.Pattern
	egress        []policy.Pattern
	modTimes      map[string]time.Time
}

// projectTable is an immutable set of snapshots swapped atomically.
// `ids` is sorted for stable output.
type projectTable struct {
	byID map[string]*projectSnapshot
	ids  []string
}

// lookupToken finds the project matching the given token. It returns
// nil if no project matches. All entries are compared in constant
// time to avoid leaking information.
func (table *projectTable) lookupToken(token string) *projectSnapshot {
	candidate := []byte(token)
	var found *projectSnapshot
	for _, id := range table.ids {
		snapshot := table.byID[id]
		if snapshot.token == "" {
			continue
		}
		if subtle.ConstantTimeCompare(candidate, []byte(snapshot.token)) == 1 {
			found = snapshot
		}
	}
	return found
}

// withProject returns a copy of the table with the given snapshot
// added or replaced. A nil snapshot removes that id.
func (table *projectTable) withProject(id string, snapshot *projectSnapshot) *projectTable {
	updated := &projectTable{byID: make(map[string]*projectSnapshot, len(table.byID)+1)}
	for existingID, existing := range table.byID {
		if existingID != id {
			updated.byID[existingID] = existing
		}
	}
	if snapshot != nil {
		updated.byID[id] = snapshot
	}
	updated.ids = sortedIDs(updated.byID)
	return updated
}

// sortedIDs returns the keys of byID sorted alphabetically.
func sortedIDs(byID map[string]*projectSnapshot) []string {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// floorState pairs the host-wide allowlist floor with the file stamp
// it was read from. The poller compares stamps to detect changes.
type floorState struct {
	patterns []policy.Pattern
	stamp    string
}

// snapshot returns the current snapshot for the given project id, or
// nil if unknown.
func (broker *Broker) snapshot(id string) *projectSnapshot {
	return broker.projects.Load().byID[id]
}

// loadProject reads a project's state files into a snapshot. Takes a
// state.Project and returns the snapshot or an error. Missing files
// are fine. Unreadable files are errors. Bad egress patterns are
// skipped and logged.
func (broker *Broker) loadProject(project *state.Project) (*projectSnapshot, error) {
	modTimes, err := project.ModTimes()
	if err != nil {
		return nil, err
	}
	token, _, err := project.TokenIfPresent()
	if err != nil {
		return nil, err
	}
	grants, err := project.Grants()
	if err != nil {
		return nil, err
	}
	egress, err := project.EgressAllow()
	if err != nil {
		return nil, err
	}
	profile, err := project.Profile()
	if err != nil {
		return nil, err
	}
	snapshot := &projectSnapshot{
		id:       project.ID,
		token:    token,
		grants:   grants,
		granted:  make(map[string]bool, len(grants)),
		profile:  profile,
		egress:   egress,
		modTimes: modTimes,
	}
	for _, name := range grants {
		snapshot.granted[name] = true
	}
	if profile != nil {
		for _, entry := range profile.Egress {
			pattern, err := policy.ParsePattern(entry)
			if err != nil {
				broker.logger.Printf("project %s: profile egress %q skipped: %v",
					project.ID, entry, err)
				continue
			}
			snapshot.profileEgress = append(snapshot.profileEgress, pattern)
		}
	}
	return snapshot, nil
}

// reloadProject re-reads the project with the given id and swaps its
// snapshot in. Drops projects whose directory is gone. Returns any
// load error, keeping the previous snapshot on failure.
func (broker *Broker) reloadProject(id string) error {
	broker.reloadMutex.Lock()
	defer broker.reloadMutex.Unlock()
	project, err := broker.root.ProjectByID(id)
	if err != nil {
		broker.projects.Store(broker.projects.Load().withProject(id, nil))
		return nil
	}
	snapshot, err := broker.loadProject(project)
	if err != nil {
		broker.noteLoadError(id, err)
		return err
	}
	delete(broker.lastLoadErrors, id)
	broker.projects.Store(broker.projects.Load().withProject(id, snapshot))
	return nil
}

// reloadAll rebuilds the whole project table from disk. Returns the
// first load error after trying all projects. Failed projects keep
// their previous snapshots.
func (broker *Broker) reloadAll() error {
	broker.reloadMutex.Lock()
	defer broker.reloadMutex.Unlock()
	projects, err := broker.root.ListProjects()
	if err != nil {
		return err
	}
	previous := broker.projects.Load()
	table := &projectTable{byID: map[string]*projectSnapshot{}}
	var firstError error
	for _, project := range projects {
		snapshot, err := broker.loadProject(project)
		if err != nil {
			broker.noteLoadError(project.ID, err)
			if firstError == nil {
				firstError = err
			}
			if kept := previous.byID[project.ID]; kept != nil {
				table.byID[project.ID] = kept
			}
			continue
		}
		delete(broker.lastLoadErrors, project.ID)
		table.byID[project.ID] = snapshot
	}
	table.ids = sortedIDs(table.byID)
	broker.projects.Store(table)
	return firstError
}

// noteLoadError logs a project load failure once per unique message.
// Repeated errors with the same text are suppressed. The caller must
// hold reloadMutex.
func (broker *Broker) noteLoadError(id string, err error) {
	message := err.Error()
	if broker.lastLoadErrors[id] == message {
		return
	}
	broker.lastLoadErrors[id] = message
	broker.logger.Printf("project %s: %s", id, message)
}

// pollOnce runs one poll cycle. It reloads the floor if changed,
// reloads projects with modified files, and drops deleted projects.
func (broker *Broker) pollOnce() {
	broker.reloadFloor()
	projects, err := broker.root.ListProjects()
	if err != nil {
		broker.logger.Print(err)
		return
	}
	table := broker.projects.Load()
	seen := make(map[string]bool, len(projects))
	for _, project := range projects {
		seen[project.ID] = true
		modTimes, err := project.ModTimes()
		if err != nil {
			broker.logger.Print(err)
			continue
		}
		existing := table.byID[project.ID]
		if existing != nil && sameModTimes(existing.modTimes, modTimes) {
			continue
		}
		broker.reloadProject(project.ID)
	}
	for _, id := range table.ids {
		if !seen[id] {
			broker.reloadProject(id)
		}
	}
}

// sameModTimes returns true if both maps have the same files with
// the same modification times.
func sameModTimes(before, after map[string]time.Time) bool {
	if len(before) != len(after) {
		return false
	}
	for name, time := range before {
		other, ok := after[name]
		if !ok || !other.Equal(time) {
			return false
		}
	}
	return true
}

// reloadFloor re-reads the host-wide egress-allow file if it has
// changed. Falls back to policy.DefaultFloor when the file is empty
// or absent. Parse errors are logged and the previous floor is kept.
func (broker *Broker) reloadFloor() {
	stamp := fileStamp(broker.root.EgressAllowFile())
	current := broker.floor.Load()
	if current.patterns != nil && current.stamp == stamp {
		return
	}
	patterns, err := broker.root.FloorAllowList()
	if err != nil {
		broker.logger.Print(err)
		if current.patterns != nil {
			return
		}
		patterns = nil
	}
	if len(patterns) == 0 {
		patterns = policy.DefaultFloor()
	}
	broker.floor.Store(&floorState{patterns: patterns, stamp: stamp})
}

// fileStamp returns a string combining the size and modification time
// of the file at path. Returns "absent" if the file cannot be stat'd.
func fileStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

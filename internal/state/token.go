package state

import "crypto/subtle"

// tokenEntry pairs a project id with its token.
type tokenEntry struct {
	projectID string
	token     string
}

// TokenTable holds all project tokens in memory. It is a snapshot;
// build a new one when tokens on disk change.
type TokenTable struct {
	entries []tokenEntry
}

// TokenTable loads every project token into a new table. Projects
// without a token file are skipped.
func (r *Root) TokenTable() (*TokenTable, error) {
	projects, err := r.ListProjects()
	if err != nil {
		return nil, err
	}
	table := &TokenTable{}
	for _, project := range projects {
		token, found, err := project.TokenIfPresent()
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		table.entries = append(table.entries, tokenEntry{
			projectID: project.ID,
			token:     token,
		})
	}
	return table, nil
}

// Lookup returns the project id for the given token. Uses constant-time
// comparison and always walks the full table.
func (t *TokenTable) Lookup(token string) (projectID string, ok bool) {
	if t == nil {
		return "", false
	}
	candidate := []byte(token)
	for _, entry := range t.entries {
		if subtle.ConstantTimeCompare(candidate, []byte(entry.token)) == 1 {
			projectID, ok = entry.projectID, true
		}
	}
	return projectID, ok
}

// Len returns the number of tokens in the table.
func (t *TokenTable) Len() int {
	if t == nil {
		return 0
	}
	return len(t.entries)
}

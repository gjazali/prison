package state

import "crypto/subtle"

type tokenEntry struct {
	projectID string
	token     string
}

// TokenTable is a snapshot of the tokens on disk at read time.
type TokenTable struct {
	entries []tokenEntry
}

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

// Lookup walks the full table with constant-time comparison so that the
// timing does not leak a token.
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

func (t *TokenTable) Len() int {
	if t == nil {
		return 0
	}
	return len(t.entries)
}

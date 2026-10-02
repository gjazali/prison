package guest

import (
	"strings"
	"sync"

	"prison/internal/policy"
)

type allowListHolder struct {
	mutex sync.RWMutex
	list  *policy.AllowList
}

func newAllowListHolder() *allowListHolder {
	return &allowListHolder{list: policy.NewAllowList()}
}

// Current returns the active allowlist. The list is immutable, so it
// stays valid after a later `Replace`.
func (h *allowListHolder) Current() *policy.AllowList {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return h.list
}

func (h *allowListHolder) Replace(list *policy.AllowList) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.list = list
}

// parseHostsAnswer parses the body of the `GET /v1/hosts` response.
func parseHostsAnswer(text string) (*policy.AllowList, int, error) {
	patterns, err := policy.ParseList(strings.NewReader(text))
	if err != nil {
		return nil, 0, err
	}
	return policy.NewAllowList(patterns), len(patterns), nil
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

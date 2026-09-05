package guest

import (
	"strings"
	"sync"

	"prison/internal/policy"
)

// allowListHolder holds the egress allowlist from the broker. The
// resolver and the tunnel share it.
type allowListHolder struct {
	mutex sync.RWMutex
	list  *policy.AllowList
}

// newAllowListHolder returns a holder with an empty list that allows
// nothing.
func newAllowListHolder() *allowListHolder {
	return &allowListHolder{list: policy.NewAllowList()}
}

// Current returns the active allowlist. The returned list is immutable,
// so it stays valid after a later Replace call.
func (h *allowListHolder) Current() *policy.AllowList {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return h.list
}

// Replace sets list as the active allowlist.
func (h *allowListHolder) Replace(list *policy.AllowList) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.list = list
}

// parseHostsAnswer parses the GET /v1/hosts response body. It takes
// the text body and returns the parsed allowlist, the pattern count,
// and an error if any line is invalid.
func parseHostsAnswer(text string) (*policy.AllowList, int, error) {
	patterns, err := policy.ParseList(strings.NewReader(text))
	if err != nil {
		return nil, 0, err
	}
	return policy.NewAllowList(patterns), len(patterns), nil
}

// normalizeName lowercases a hostname and strips one trailing dot.
// It takes a hostname string and returns the normalized form.
func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

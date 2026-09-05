package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TrustRecord holds the approved hash and optional origin of one
// installed item.
type TrustRecord struct {
	Hash      string `json:"hash"`
	OriginURL string `json:"origin_url,omitempty"`
	OriginRef string `json:"origin_ref,omitempty"`
}

// TrustRecord reads the trust record for the given kind and name.
// Returns nil with no error if no record exists.
func (r *Root) TrustRecord(kind, name string) (*TrustRecord, error) {
	path, err := r.trustRecordFile(kind, name)
	if err != nil {
		return nil, err
	}
	var record TrustRecord
	found, err := readJSONFile(path, &record)
	if err != nil || !found {
		return nil, err
	}
	return &record, nil
}

// SaveTrustRecord writes a trust record for the given kind and name,
// creating the directory if needed.
func (r *Root) SaveTrustRecord(kind, name string, record *TrustRecord) error {
	path, err := r.trustRecordFile(kind, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), directoryMode); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(path), err)
	}
	return writeJSONFile(path, record)
}

// DeleteTrustRecord removes the trust record for the given kind and
// name. Does nothing if no record exists.
func (r *Root) DeleteTrustRecord(kind, name string) error {
	path, err := r.trustRecordFile(kind, name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove %s: %w", path, err)
	}
	return nil
}

// trustRecordFile returns the file path for a trust record. Returns an
// error if kind or name would escape the trust directory.
func (r *Root) trustRecordFile(kind, name string) (string, error) {
	if err := checkTrustComponent("kind", kind); err != nil {
		return "", err
	}
	if err := checkTrustComponent("name", name); err != nil {
		return "", err
	}
	return filepath.Join(r.TrustDir(), kind+"s", name+".json"), nil
}

// checkTrustComponent rejects a value that is empty, contains a path
// separator, or starts with a dot. label names the part for the error.
func checkTrustComponent(label, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("a trust record needs a %s", label)
	case strings.ContainsAny(value, `/\`) || strings.HasPrefix(value, "."):
		return fmt.Errorf("%q is not a usable trust record %s", value, label)
	}
	return nil
}

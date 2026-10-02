package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type TrustRecord struct {
	Hash      string `json:"hash"`
	OriginURL string `json:"origin_url,omitempty"`
	OriginRef string `json:"origin_ref,omitempty"`
}

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

func (r *Root) trustRecordFile(kind, name string) (string, error) {
	if err := checkTrustComponent("kind", kind); err != nil {
		return "", err
	}
	if err := checkTrustComponent("name", name); err != nil {
		return "", err
	}
	return filepath.Join(r.TrustDir(), kind+"s", name+".json"), nil
}

func checkTrustComponent(label, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("trust record has no %s", label)
	case strings.ContainsAny(value, `/\`) || strings.HasPrefix(value, "."):
		return fmt.Errorf("trust record %s %q is not valid", label, value)
	}
	return nil
}

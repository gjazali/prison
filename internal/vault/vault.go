package vault

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const FormatVersion = 2

const (
	keyDerivationName        = "argon2id"
	defaultArgon2Time        = 3
	defaultArgon2MemoryKiB   = 64 * 1024
	defaultArgon2Threads     = 1
	saltLength               = 16
	derivedKeyLength         = chacha20poly1305.KeySize
	temporaryFileNamePattern = ".secrets.vault-*"
)

const vaultFileMode fs.FileMode = 0o600

// ErrWrongPassphrase also covers a tampered file, because both fail
// authentication.
var ErrWrongPassphrase = errors.New(
	"wrong passphrase or tampered vault")

var ErrLocked = errors.New("the vault is locked")

type keyDerivationParameters struct {
	Name      string `json:"name"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

// containerHeader in compact JSON is the AEAD additional data. A change
// to these fields makes decryption fail.
type containerHeader struct {
	Version int                     `json:"version"`
	KDF     keyDerivationParameters `json:"kdf"`
	Salt    string                  `json:"salt"`
}

type container struct {
	Version    int                     `json:"version"`
	KDF        keyDerivationParameters `json:"kdf"`
	Salt       string                  `json:"salt"`
	Nonce      string                  `json:"nonce"`
	Ciphertext string                  `json:"ciphertext"`
}

func (c *container) header() containerHeader {
	return containerHeader{Version: c.Version, KDF: c.KDF, Salt: c.Salt}
}

// Vault keeps the identity of the file that it loaded, so reads can
// detect an external write and reload. A Vault must not be copied.
type Vault struct {
	path          string
	mutex         sync.Mutex
	key           []byte
	salt          []byte
	kdf           keyDerivationParameters
	document      *Document
	loadedModTime time.Time
	loadedSize    int64
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func Create(path, passphrase string) (*Vault, error) {
	if passphrase == "" {
		return nil, errors.New("the passphrase is empty")
	}
	if Exists(path) {
		return nil, fmt.Errorf("a vault exists at %s", path)
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("cannot generate a vault salt: %w", err)
	}
	parameters := keyDerivationParameters{
		Name:      keyDerivationName,
		Time:      defaultArgon2Time,
		MemoryKiB: defaultArgon2MemoryKiB,
		Threads:   defaultArgon2Threads,
	}
	vault := &Vault{
		path: path,
		key:  deriveKey(passphrase, salt, parameters),
		salt: salt,
		kdf:  parameters,
	}
	if err := vault.writeLocked(emptyDocument()); err != nil {
		return nil, err
	}
	return vault, nil
}

func Open(path, passphrase string) (*Vault, error) {
	parsed, info, err := readContainer(path)
	if err != nil {
		return nil, err
	}
	salt, err := base64.StdEncoding.DecodeString(parsed.Salt)
	if err != nil || len(salt) == 0 {
		return nil, fmt.Errorf("%s has an unreadable salt", path)
	}
	vault := &Vault{
		path: path,
		key:  deriveKey(passphrase, salt, parsed.KDF),
		salt: salt,
		kdf:  parsed.KDF,
	}
	document, err := decryptDocument(parsed, vault.key)
	if err != nil {
		return nil, err
	}
	vault.setLoaded(document, info)
	return vault, nil
}

// Lock zeroes the key as a best effort, because the Go runtime can keep
// copies of it.
func (v *Vault) Lock() {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	for index := range v.key {
		v.key[index] = 0
	}
	v.key = nil
	v.document = nil
}

// Reload drops the cached document on error so that a stale document is
// never used.
func (v *Vault) Reload() error {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	return v.reloadLocked()
}

func (v *Vault) Secret(name string) (*Secret, bool) {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	if err := v.reloadLocked(); err != nil {
		return nil, false
	}
	secret, found := v.document.Secrets[name]
	if !found || secret == nil {
		return nil, false
	}
	return copySecret(secret), true
}

func (v *Vault) Secrets() []*Secret {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	if err := v.reloadLocked(); err != nil {
		return nil
	}
	names := make([]string, 0, len(v.document.Secrets))
	for name, secret := range v.document.Secrets {
		if secret != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	secrets := make([]*Secret, 0, len(names))
	for _, name := range names {
		secrets = append(secrets, copySecret(v.document.Secrets[name]))
	}
	return secrets
}

func (v *Vault) PutSecret(secret *Secret) (replaced bool, err error) {
	if err := ValidateSecret(secret); err != nil {
		return false, err
	}
	if secret.Created.IsZero() {
		secret.Created = time.Now()
	}
	v.mutex.Lock()
	defer v.mutex.Unlock()
	if err := v.reloadLocked(); err != nil {
		return false, err
	}
	document := copyDocument(v.document)
	_, replaced = document.Secrets[secret.Name]
	document.Secrets[secret.Name] = copySecret(secret)
	if err := v.writeLocked(document); err != nil {
		return false, err
	}
	return replaced, nil
}

func (v *Vault) DeleteSecret(name string) (bool, error) {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	if err := v.reloadLocked(); err != nil {
		return false, err
	}
	if _, found := v.document.Secrets[name]; !found {
		return false, nil
	}
	document := copyDocument(v.document)
	delete(document.Secrets, name)
	if err := v.writeLocked(document); err != nil {
		return false, err
	}
	return true, nil
}

func (v *Vault) Authority(host string) (*Authority, bool) {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	if err := v.reloadLocked(); err != nil {
		return nil, false
	}
	authority, found := v.document.Authorities[host]
	if !found || authority == nil {
		return nil, false
	}
	return copyAuthority(authority), true
}

func (v *Vault) SetAuthority(host string, authority *Authority) error {
	if host == "" {
		return errors.New("an authority needs a host")
	}
	if authority == nil || authority.KeyPEM == "" ||
		authority.CertificatePEM == "" {
		return fmt.Errorf(
			"the authority for %s needs a key and a certificate", host)
	}
	stored := copyAuthority(authority)
	if stored.Created.IsZero() {
		stored.Created = time.Now()
	}
	v.mutex.Lock()
	defer v.mutex.Unlock()
	if err := v.reloadLocked(); err != nil {
		return err
	}
	document := copyDocument(v.document)
	document.Authorities[host] = stored
	return v.writeLocked(document)
}

func (v *Vault) reloadLocked() error {
	if v.key == nil {
		return ErrLocked
	}
	info, err := os.Stat(v.path)
	if err != nil {
		v.document = nil
		return fmt.Errorf("cannot read the vault: %w", err)
	}
	if v.document != nil && info.Size() == v.loadedSize &&
		info.ModTime().Equal(v.loadedModTime) {
		return nil
	}
	parsed, info, err := readContainer(v.path)
	if err != nil {
		v.document = nil
		return err
	}
	document, err := decryptDocument(parsed, v.key)
	if err != nil {
		v.document = nil
		return fmt.Errorf("cannot reload %s: %w", v.path, err)
	}
	v.setLoaded(document, info)
	return nil
}

func (v *Vault) setLoaded(document *Document, info fs.FileInfo) {
	v.document = document
	v.loadedModTime = info.ModTime()
	v.loadedSize = info.Size()
}

func (v *Vault) writeLocked(document *Document) error {
	if v.key == nil {
		return ErrLocked
	}
	encoded, err := encryptDocument(document, v.key, v.salt, v.kdf)
	if err != nil {
		return err
	}
	info, err := replaceFile(v.path, encoded)
	if err != nil {
		return err
	}
	v.setLoaded(document, info)
	return nil
}

func deriveKey(passphrase string, salt []byte,
	parameters keyDerivationParameters) []byte {
	return argon2.IDKey([]byte(passphrase), salt, parameters.Time,
		parameters.MemoryKiB, parameters.Threads, derivedKeyLength)
}

func readContainer(path string) (*container, fs.FileInfo, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf(
			"no vault at %s. Run `prison secret init`", path)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read the vault: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read the vault: %w", err)
	}
	var parsed container
	if err := json.NewDecoder(file).Decode(&parsed); err != nil {
		return nil, nil, fmt.Errorf("%s is not a vault file: %w", path, err)
	}
	if parsed.Version != FormatVersion {
		return nil, nil, fmt.Errorf(
			"%s is a version %d vault, but this prison reads version %d",
			path, parsed.Version, FormatVersion)
	}
	if parsed.KDF.Name != keyDerivationName || parsed.KDF.Time == 0 ||
		parsed.KDF.MemoryKiB == 0 || parsed.KDF.Threads == 0 {
		return nil, nil, fmt.Errorf(
			"%s uses an unsupported key derivation %q",
			path, parsed.KDF.Name)
	}
	return &parsed, info, nil
}

func decryptDocument(parsed *container, key []byte) (*Document, error) {
	nonce, err := base64.StdEncoding.DecodeString(parsed.Nonce)
	if err != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, errors.New("the vault file has an unreadable nonce")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(parsed.Ciphertext)
	if err != nil {
		return nil, errors.New("the vault file has unreadable ciphertext")
	}
	additionalData, err := json.Marshal(parsed.header())
	if err != nil {
		return nil, fmt.Errorf("cannot encode the vault header: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize the vault cipher: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, additionalData)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	document := emptyDocument()
	if err := json.Unmarshal(plaintext, document); err != nil {
		return nil, fmt.Errorf(
			"the decrypted vault is not valid: %w", err)
	}
	if document.Secrets == nil {
		document.Secrets = map[string]*Secret{}
	}
	if document.Authorities == nil {
		document.Authorities = map[string]*Authority{}
	}
	return document, nil
}

func encryptDocument(document *Document, key, salt []byte,
	parameters keyDerivationParameters) ([]byte, error) {
	plaintext, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("cannot encode the vault document: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("cannot generate a vault nonce: %w", err)
	}
	header := containerHeader{
		Version: FormatVersion,
		KDF:     parameters,
		Salt:    base64.StdEncoding.EncodeToString(salt),
	}
	additionalData, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("cannot encode the vault header: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize the vault cipher: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, additionalData)
	encoded, err := json.MarshalIndent(container{
		Version:    header.Version,
		KDF:        header.KDF,
		Salt:       header.Salt,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cannot encode the vault file: %w", err)
	}
	return append(encoded, '\n'), nil
}

// replaceFile returns the info of the temporary file. The rename keeps its
// modification time and size.
func replaceFile(path string, content []byte) (fs.FileInfo, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, temporaryFileNamePattern)
	if err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	info, err := writeAndStat(temporary, content)
	if err != nil {
		temporary.Close()
		os.Remove(temporaryPath)
		return nil, fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return nil, fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		os.Remove(temporaryPath)
		return nil, fmt.Errorf("cannot replace %s: %w", path, err)
	}
	return info, nil
}

func writeAndStat(file *os.File, content []byte) (fs.FileInfo, error) {
	if err := file.Chmod(vaultFileMode); err != nil {
		return nil, err
	}
	if _, err := file.Write(content); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	return file.Stat()
}

func emptyDocument() *Document {
	return &Document{
		Version:     FormatVersion,
		Secrets:     map[string]*Secret{},
		Authorities: map[string]*Authority{},
	}
}

// copyDocument makes a deep copy so that a write never changes the
// cache that concurrent readers copy from.
func copyDocument(document *Document) *Document {
	copied := &Document{
		Version:     FormatVersion,
		Secrets:     make(map[string]*Secret, len(document.Secrets)),
		Authorities: make(map[string]*Authority, len(document.Authorities)),
	}
	for name, secret := range document.Secrets {
		if secret != nil {
			copied.Secrets[name] = copySecret(secret)
		}
	}
	for host, authority := range document.Authorities {
		if authority != nil {
			copied.Authorities[host] = copyAuthority(authority)
		}
	}
	return copied
}

func copySecret(secret *Secret) *Secret {
	copied := *secret
	if secret.Route != nil {
		route := *secret.Route
		if secret.Route.Methods != nil {
			route.Methods = append([]string(nil), secret.Route.Methods...)
		}
		copied.Route = &route
	}
	if secret.Sign != nil {
		sign := *secret.Sign
		copied.Sign = &sign
	}
	if secret.Expose != nil {
		expose := *secret.Expose
		copied.Expose = &expose
	}
	return &copied
}

func copyAuthority(authority *Authority) *Authority {
	copied := *authority
	return &copied
}

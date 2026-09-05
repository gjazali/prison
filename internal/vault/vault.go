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

// FormatVersion is the container version this package reads and writes.
const FormatVersion = 2

// Key derivation defaults for new vaults.
const (
	keyDerivationName        = "argon2id"
	defaultArgon2Time        = 3
	defaultArgon2MemoryKiB   = 64 * 1024
	defaultArgon2Threads     = 1
	saltLength               = 16
	derivedKeyLength         = chacha20poly1305.KeySize
	temporaryFileNamePattern = ".secrets.vault-*"
)

// vaultFileMode is the file permission for vault files.
const vaultFileMode fs.FileMode = 0o600

// ErrWrongPassphrase means the key did not authenticate the
// ciphertext. A tampered file looks the same.
var ErrWrongPassphrase = errors.New(
	"wrong passphrase, or the vault has been tampered with")

// ErrLocked is returned by operations on a locked vault.
var ErrLocked = errors.New("the vault is locked")

// keyDerivationParameters is the kdf object in the container header.
// Every field is read back from the file when unlocking.
type keyDerivationParameters struct {
	Name      string `json:"name"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

// containerHeader is the authenticated part of the container. Its
// compact JSON encoding is the additional data for the AEAD, so any
// change to these fields after writing fails decryption.
type containerHeader struct {
	Version int                     `json:"version"`
	KDF     keyDerivationParameters `json:"kdf"`
	Salt    string                  `json:"salt"`
}

// container is the on-disk file. Salt, Nonce, and Ciphertext are
// standard base64.
type container struct {
	Version    int                     `json:"version"`
	KDF        keyDerivationParameters `json:"kdf"`
	Salt       string                  `json:"salt"`
	Nonce      string                  `json:"nonce"`
	Ciphertext string                  `json:"ciphertext"`
}

// header returns the authenticated subset of the container.
func (c *container) header() containerHeader {
	return containerHeader{Version: c.Version, KDF: c.KDF, Salt: c.Salt}
}

// Vault is an unlocked secret store. It holds the derived key, the
// decrypted document, and the file identity the document was loaded
// from, so reads can notice an external write and reload. Every method
// takes the mutex; a Vault must not be copied after creation.
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

// Exists reports whether a file is present at path. It does not check
// that the file is a readable vault.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Create writes a new empty vault at path, encrypted under passphrase
// with the default key derivation parameters, and returns it unlocked.
// It refuses an empty passphrase and refuses to replace an existing
// file. Errors from key derivation or the write are returned with the
// path.
func Create(path, passphrase string) (*Vault, error) {
	if passphrase == "" {
		return nil, errors.New("a vault needs a passphrase; none was given")
	}
	if Exists(path) {
		return nil, fmt.Errorf(
			"a vault already exists at %s; remove it first to start over",
			path)
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

// Open reads the vault at path, derives the key from passphrase using
// the parameters recorded in the file, and decrypts the document once.
// It returns ErrWrongPassphrase when the ciphertext does not
// authenticate, and a descriptive error when the file is missing,
// malformed, or written by another format version.
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

// Lock forgets the key and the decrypted document. Every later method
// returns ErrLocked or reports nothing found. The zeroing is best
// effort, since the Go runtime may have copied the key.
func (v *Vault) Lock() {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	for index := range v.key {
		v.key[index] = 0
	}
	v.key = nil
	v.document = nil
}

// Reload re-reads and decrypts the file when its modification time or
// size differs from the loaded copy, or when nothing is cached. It
// returns ErrLocked on a locked vault and otherwise any read or
// decryption error, after which the cache is dropped so a stale
// document is never served.
func (v *Vault) Reload() error {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	return v.reloadLocked()
}

// Secret returns a copy of the named secret and whether it exists. The
// file is reloaded first; a reload failure reads as not found.
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

// Secrets returns copies of every secret sorted by name. The file is
// reloaded first; a reload failure yields an empty slice.
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

// PutSecret validates and normalises secret through ValidateSecret,
// stores a copy under its name, and writes the vault. A zero Created is
// stamped with the current time on the stored copy and on secret
// itself. It reports whether a secret of that name was replaced, and
// returns validation, reload, or write errors.
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

// DeleteSecret removes the named secret and writes the vault. It
// reports whether the secret existed; when it did not, nothing is
// written. Reload and write errors are returned.
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

// Authority returns a copy of the certificate authority for host and
// whether one exists. The file is reloaded first; a reload failure
// reads as not found.
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

// SetAuthority stores a copy of authority under host and writes the
// vault, replacing any previous authority for that host. A zero Created
// is stamped with the current time. It refuses an empty host, a nil
// authority, or one missing either PEM half, and returns reload or
// write errors.
func (v *Vault) SetAuthority(host string, authority *Authority) error {
	if host == "" {
		return errors.New("an authority needs a host to belong to")
	}
	if authority == nil || authority.KeyPEM == "" ||
		authority.CertificatePEM == "" {
		return fmt.Errorf(
			"the authority for %s needs both a key and a certificate", host)
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

// reloadLocked is Reload without taking the mutex. The caller holds it.
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

// setLoaded installs document as the cache together with the file
// identity it came from.
func (v *Vault) setLoaded(document *Document, info fs.FileInfo) {
	v.document = document
	v.loadedModTime = info.ModTime()
	v.loadedSize = info.Size()
}

// writeLocked encrypts document under the vault key with a fresh nonce
// and atomically replaces the file, then caches document. The caller
// holds the mutex. It returns ErrLocked on a locked vault and any
// encoding or filesystem error with the path.
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

// deriveKey runs argon2id over passphrase and salt with parameters and
// returns the AEAD key.
func deriveKey(passphrase string, salt []byte,
	parameters keyDerivationParameters) []byte {
	return argon2.IDKey([]byte(passphrase), salt, parameters.Time,
		parameters.MemoryKiB, parameters.Threads, derivedKeyLength)
}

// readContainer parses the file at path without decrypting it and
// returns the container with the file info it was read from. It
// reports a missing file, unreadable JSON, a version other than
// FormatVersion, and key derivation parameters this package cannot run.
func readContainer(path string) (*container, fs.FileInfo, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf(
			"no vault at %s; run `prison secret init` to create one", path)
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
			"%s is a version %d vault from another prison version, and "+
				"this one reads version %d",
			path, parsed.Version, FormatVersion)
	}
	if parsed.KDF.Name != keyDerivationName || parsed.KDF.Time == 0 ||
		parsed.KDF.MemoryKiB == 0 || parsed.KDF.Threads == 0 {
		return nil, nil, fmt.Errorf(
			"%s asks for key derivation %q with parameters this prison "+
				"cannot run", path, parsed.KDF.Name)
	}
	return &parsed, info, nil
}

// decryptDocument authenticates and decrypts parsed with key, using the
// header as additional data, and returns the document with nil maps
// replaced by empty ones. It returns ErrWrongPassphrase when
// authentication fails and a descriptive error for malformed fields or
// plaintext.
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
		return nil, fmt.Errorf("cannot initialise the vault cipher: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, additionalData)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	document := emptyDocument()
	if err := json.Unmarshal(plaintext, document); err != nil {
		return nil, fmt.Errorf(
			"the vault decrypted to something that is not a vault: %w", err)
	}
	if document.Secrets == nil {
		document.Secrets = map[string]*Secret{}
	}
	if document.Authorities == nil {
		document.Authorities = map[string]*Authority{}
	}
	return document, nil
}

// encryptDocument serialises document, encrypts it under key with a
// fresh random nonce and the header as additional data, and returns the
// indented container JSON ready to write. It returns encoding, random,
// or cipher errors.
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
		return nil, fmt.Errorf("cannot initialise the vault cipher: %w", err)
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

// replaceFile writes content to a temporary file beside path at mode
// 0600, syncs it, and renames it over path, creating the parent
// directory if needed. It returns the info of the written file, whose
// modification time and size survive the rename, so the caller can
// record the identity of exactly what it wrote. Filesystem errors are
// returned with the path and the temporary file is removed.
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

// writeAndStat sets file to mode 0600, writes content, syncs, and
// returns the resulting file info. The caller closes file.
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

// emptyDocument returns the document a new vault holds.
func emptyDocument() *Document {
	return &Document{
		Version:     FormatVersion,
		Secrets:     map[string]*Secret{},
		Authorities: map[string]*Authority{},
	}
}

// copyDocument returns a deep copy of document so a write never mutates
// the cache that concurrent readers may be copying from.
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

// copySecret returns a deep copy of secret, including its mode spec and
// method list.
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

// copyAuthority returns a copy of authority.
func copyAuthority(authority *Authority) *Authority {
	copied := *authority
	return &copied
}

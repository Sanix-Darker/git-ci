// Package secrets encrypts project secret values before they reach SQLite.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sanix-darker/git-ci/internal/store"
)

const algorithm = "AES-256-GCM"

var secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

type Manager struct {
	store *store.Store
	aead  cipher.AEAD
}

func NewManager(database *store.Store, keyPath string) (*Manager, error) {
	if database == nil {
		return nil, errors.New("secrets: store is required")
	}
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: initialize cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: initialize GCM: %w", err)
	}
	return &Manager{store: database, aead: aead}, nil
}

func (m *Manager) Upsert(ctx context.Context, projectID, name, value string) (store.Secret, error) {
	name = strings.TrimSpace(name)
	if !secretName.MatchString(name) {
		return store.Secret{}, errors.New("secrets: name must match [A-Z_][A-Z0-9_]*")
	}
	if value == "" {
		return store.Secret{}, errors.New("secrets: value must not be empty")
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return store.Secret{}, fmt.Errorf("secrets: generate nonce: %w", err)
	}
	plaintext := []byte(value)
	ciphertext := m.aead.Seal(nil, nonce, plaintext, associatedData(projectID, name))
	clear(plaintext)
	provider := "local"
	version := time.Now().UTC().Format(time.RFC3339Nano)
	return m.store.UpsertSecret(ctx, store.UpsertSecretParams{
		ProjectID: projectID, Name: name, Provider: &provider, Version: &version,
		EncryptionAlgorithm: algorithm, Nonce: nonce, Ciphertext: ciphertext,
	})
}

func (m *Manager) List(ctx context.Context, projectID string) ([]store.Secret, error) {
	return m.store.ListSecrets(ctx, projectID)
}

func (m *Manager) Delete(ctx context.Context, secretID string) error {
	return m.store.DeleteSecret(ctx, secretID)
}

// ResolveProject decrypts values only for the worker process. Callers must not
// serialize the returned map or persist it in a run snapshot.
func (m *Manager) ResolveProject(ctx context.Context, projectID string) (map[string]string, error) {
	metadata, err := m.store.ListSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(metadata))
	for _, secret := range metadata {
		envelope, err := m.store.GetSecretEnvelope(ctx, secret.ID)
		if err != nil {
			return nil, err
		}
		if envelope.EncryptionAlgorithm != algorithm {
			return nil, fmt.Errorf("secrets: unsupported encryption algorithm %q", envelope.EncryptionAlgorithm)
		}
		plaintext, err := m.aead.Open(nil, envelope.Nonce, envelope.Ciphertext, associatedData(envelope.ProjectID, envelope.Name))
		if err != nil {
			return nil, fmt.Errorf("secrets: decrypt %s: %w", envelope.Name, err)
		}
		values[envelope.Name] = string(plaintext)
		clear(plaintext)
	}
	return values, nil
}

func associatedData(projectID, name string) []byte {
	return []byte(projectID + "\x00" + name)
}

func loadOrCreateKey(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("secrets: key path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secrets: create key directory: %w", err)
	}
	key, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, fmt.Errorf("secrets: stat key: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || len(key) != 32 {
			clear(key)
			return nil, errors.New("secrets: key must be a 32-byte mode-0600 regular file")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("secrets: read key: %w", err)
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("secrets: generate key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		clear(key)
		return nil, fmt.Errorf("secrets: create key: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		clear(key)
		return nil, fmt.Errorf("secrets: write key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		clear(key)
		return nil, fmt.Errorf("secrets: sync key: %w", err)
	}
	if err := file.Close(); err != nil {
		clear(key)
		return nil, fmt.Errorf("secrets: close key: %w", err)
	}
	return key, nil
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

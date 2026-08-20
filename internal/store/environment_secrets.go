package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type EnvironmentSecret struct {
	ID                  string    `json:"id"`
	EnvironmentID       string    `json:"environmentId"`
	Name                string    `json:"name"`
	Provider            *string   `json:"provider,omitempty"`
	Version             *string   `json:"version,omitempty"`
	EncryptionAlgorithm string    `json:"encryptionAlgorithm"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type EnvironmentSecretEnvelope struct {
	EnvironmentSecret
	Nonce      []byte `json:"-"`
	Ciphertext []byte `json:"-"`
}

type UpsertEnvironmentSecretParams struct {
	EnvironmentID       string
	Name                string
	Provider            *string
	Version             *string
	EncryptionAlgorithm string
	Nonce               []byte
	Ciphertext          []byte
}

const environmentSecretColumns = `id, environment_id, name, provider, version, encryption_algorithm, created_at, updated_at`

func (s *Store) UpsertEnvironmentSecret(ctx context.Context, params UpsertEnvironmentSecretParams) (EnvironmentSecret, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return EnvironmentSecret{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return EnvironmentSecret{}, err
	}
	params.EnvironmentID, err = normalizeRequiredText("environment secret environment ID", params.EnvironmentID)
	if err != nil {
		return EnvironmentSecret{}, err
	}
	params.Name, err = normalizeRequiredText("environment secret name", params.Name)
	if err != nil {
		return EnvironmentSecret{}, err
	}
	params.EncryptionAlgorithm, err = normalizeRequiredText("environment secret encryption algorithm", params.EncryptionAlgorithm)
	if err != nil {
		return EnvironmentSecret{}, err
	}
	if len(params.Nonce) == 0 || len(params.Ciphertext) == 0 {
		return EnvironmentSecret{}, invalidInput("environment secret envelope", "nonce and ciphertext are required")
	}
	id, err := randomOpaqueID()
	if err != nil {
		return EnvironmentSecret{}, fmt.Errorf("store: generate environment secret ID: %w", err)
	}
	now := nowUTC()
	_, err = db.ExecContext(ctx, `
		INSERT INTO environment_secret_envelopes (
			id, environment_id, name, provider, version, encryption_algorithm, nonce, ciphertext, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(environment_id, name) DO UPDATE SET
			provider = excluded.provider, version = excluded.version,
			encryption_algorithm = excluded.encryption_algorithm,
			nonce = excluded.nonce, ciphertext = excluded.ciphertext, updated_at = excluded.updated_at
	`, id, params.EnvironmentID, params.Name, nullableString(params.Provider), nullableString(params.Version),
		params.EncryptionAlgorithm, params.Nonce, params.Ciphertext, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return EnvironmentSecret{}, fmt.Errorf("store: upsert environment secret: %w", err)
	}
	return s.getEnvironmentSecretByName(ctx, db, params.EnvironmentID, params.Name)
}

func (s *Store) ListEnvironmentSecrets(ctx context.Context, environmentID string) ([]EnvironmentSecret, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	environmentID, err = normalizeRequiredText("environment secret environment ID", environmentID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+environmentSecretColumns+` FROM environment_secret_envelopes WHERE environment_id = ? ORDER BY name ASC, id ASC`, environmentID)
	if err != nil {
		return nil, fmt.Errorf("store: list environment secrets: %w", err)
	}
	defer rows.Close()
	secrets := make([]EnvironmentSecret, 0)
	for rows.Next() {
		secret, scanErr := scanEnvironmentSecret(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan environment secret: %w", scanErr)
		}
		secrets = append(secrets, secret)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate environment secrets: %w", err)
	}
	return secrets, nil
}

func (s *Store) GetEnvironmentSecretEnvelope(ctx context.Context, secretID string) (EnvironmentSecretEnvelope, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return EnvironmentSecretEnvelope{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return EnvironmentSecretEnvelope{}, err
	}
	secretID, err = normalizeRequiredText("environment secret ID", secretID)
	if err != nil {
		return EnvironmentSecretEnvelope{}, err
	}
	var envelope EnvironmentSecretEnvelope
	var provider, version sql.NullString
	var createdAt, updatedAt int64
	err = db.QueryRowContext(ctx, `
		SELECT id, environment_id, name, provider, version, encryption_algorithm, nonce, ciphertext, created_at, updated_at
		FROM environment_secret_envelopes WHERE id = ?
	`, secretID).Scan(&envelope.ID, &envelope.EnvironmentID, &envelope.Name, &provider, &version,
		&envelope.EncryptionAlgorithm, &envelope.Nonce, &envelope.Ciphertext, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return EnvironmentSecretEnvelope{}, &ErrNotFound{Resource: "environment secret", Key: secretID}
	}
	if err != nil {
		return EnvironmentSecretEnvelope{}, fmt.Errorf("store: get environment secret envelope: %w", err)
	}
	envelope.Provider = nullStringPointer(provider)
	envelope.Version = nullStringPointer(version)
	envelope.CreatedAt = timeFromMillis(createdAt)
	envelope.UpdatedAt = timeFromMillis(updatedAt)
	return envelope, nil
}

func (s *Store) DeleteEnvironmentSecret(ctx context.Context, secretID string) error {
	if err := validateConfigurationContext(ctx); err != nil {
		return err
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	secretID, err = normalizeRequiredText("environment secret ID", secretID)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM environment_secret_envelopes WHERE id = ?`, secretID)
	if err != nil {
		return fmt.Errorf("store: delete environment secret: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: count deleted environment secret: %w", err)
	}
	if deleted == 0 {
		return &ErrNotFound{Resource: "environment secret", Key: secretID}
	}
	return nil
}

func (s *Store) getEnvironmentSecretByName(ctx context.Context, db *sql.DB, environmentID, name string) (EnvironmentSecret, error) {
	secret, err := scanEnvironmentSecret(db.QueryRowContext(ctx, `SELECT `+environmentSecretColumns+` FROM environment_secret_envelopes WHERE environment_id = ? AND name = ?`, environmentID, name))
	if err != nil {
		return EnvironmentSecret{}, fmt.Errorf("store: get environment secret by name: %w", err)
	}
	return secret, nil
}

func scanEnvironmentSecret(scanner configurationScanner) (EnvironmentSecret, error) {
	var secret EnvironmentSecret
	var provider, version sql.NullString
	var createdAt, updatedAt int64
	if err := scanner.Scan(&secret.ID, &secret.EnvironmentID, &secret.Name, &provider, &version,
		&secret.EncryptionAlgorithm, &createdAt, &updatedAt); err != nil {
		return EnvironmentSecret{}, err
	}
	secret.Provider = nullStringPointer(provider)
	secret.Version = nullStringPointer(version)
	secret.CreatedAt = timeFromMillis(createdAt)
	secret.UpdatedAt = timeFromMillis(updatedAt)
	return secret, nil
}

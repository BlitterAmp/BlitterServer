package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BlitterAmp/BlitterServer/internal/logging"
)

var integrationCredentialKeys = []string{
	"lastfm_api_key",
	"lastfm_shared_secret",
	"fanart_api_key",
}

const credentialSidecarMagic = "BIC1"

func isIntegrationCredential(key string) bool {
	for _, candidate := range integrationCredentialKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

func (s *Store) credentialSidecarPath() string {
	return filepath.Join(s.dataDir, "integration-credentials.enc")
}

func (s *Store) seedIntegrationCredentials(ctx context.Context) {
	credentials, err := s.readCredentialSidecar()
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		logging.From(ctx).Warn("ignoring unreadable integration credential sidecar", "err", err)
		return
	}
	for _, key := range integrationCredentialKeys {
		if value := credentials[key]; value != "" {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)`, key, value); err != nil {
				logging.From(ctx).Warn("seed integration credential", "setting", key, "err", err)
			}
		}
	}
}

func (s *Store) readCredentialSidecar() (map[string]string, error) {
	raw, err := os.ReadFile(s.credentialSidecarPath())
	if err != nil {
		return nil, err
	}
	if len(raw) < len(credentialSidecarMagic)+s.secret.NonceSize() || string(raw[:len(credentialSidecarMagic)]) != credentialSidecarMagic {
		return nil, errors.New("invalid integration credential sidecar")
	}
	nonceStart := len(credentialSidecarMagic)
	nonceEnd := nonceStart + s.secret.NonceSize()
	plaintext, err := s.secret.Open(nil, raw[nonceStart:nonceEnd], raw[nonceEnd:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt integration credential sidecar: %w", err)
	}
	var credentials map[string]string
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return nil, fmt.Errorf("decode integration credential sidecar: %w", err)
	}
	return credentials, nil
}

func (s *Store) writeCredentialSidecar(ctx context.Context) error {
	credentials := make(map[string]string)
	for _, key := range integrationCredentialKeys {
		value, ok, err := s.GetSetting(ctx, key)
		if err != nil {
			return fmt.Errorf("read integration credential %s: %w", key, err)
		}
		if ok && value != "" {
			credentials[key] = value
		}
	}
	plaintext, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.secret.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate integration credential nonce: %w", err)
	}
	raw := append([]byte(credentialSidecarMagic), nonce...)
	raw = s.secret.Seal(raw, nonce, plaintext, nil)

	tmp, err := os.CreateTemp(s.dataDir, ".integration-credentials-*")
	if err != nil {
		return fmt.Errorf("create integration credential sidecar: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, s.credentialSidecarPath()); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish integration credential sidecar: %w", err)
	}
	return nil
}

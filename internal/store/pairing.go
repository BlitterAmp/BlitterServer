package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// Sentinel errors the HTTP layer maps to 404 / 410.
var (
	ErrNotFound = errors.New("not found")
	ErrGone     = errors.New("expired or already used")
)

const (
	pairingTTL  = 10 * time.Minute
	pairCodeTTL = 15 * time.Minute
)

// codeAlphabet omits ambiguous glyphs (0/O, 1/I/L, U/V confusion) for codes
// humans read off a screen.
const codeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

func newCode() string {
	b := make([]byte, 6)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		if err != nil {
			panic(err) // crypto/rand failure is unrecoverable
		}
		b[i] = codeAlphabet[n.Int64()]
	}
	return string(b)
}

// Pairing is a PIN-pairing request awaiting admin action. Status is one of
// pending|approved|denied|expired; expired is computed on read, never stored.
type Pairing struct {
	PairingID   string
	Code        string
	DeviceName  string
	DeviceType  string
	AppVersion  string
	Status      string
	RequestedAt time.Time
	ExpiresAt   time.Time
}

// StartPairing records a pending request and returns the code to show the user.
func (s *Store) StartPairing(ctx context.Context, deviceName, deviceType, appVersion string) (Pairing, error) {
	now := time.Now().UTC()
	p := Pairing{
		PairingID:   NewID("pair"),
		Code:        newCode(),
		DeviceName:  deviceName,
		DeviceType:  deviceType,
		AppVersion:  appVersion,
		Status:      "pending",
		RequestedAt: now,
		ExpiresAt:   now.Add(pairingTTL),
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pairings (pairing_id, code, device_name, device_type, app_version, status, requested_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`,
		p.PairingID, p.Code, p.DeviceName, p.DeviceType, p.AppVersion,
		p.RequestedAt.Format(time.RFC3339), p.ExpiresAt.Format(time.RFC3339))
	if err != nil {
		return Pairing{}, err
	}
	return p, nil
}

func scanPairing(row *sql.Row) (Pairing, error) {
	var p Pairing
	var requestedAt, expiresAt string
	err := row.Scan(&p.PairingID, &p.Code, &p.DeviceName, &p.DeviceType, &p.AppVersion, &p.Status, &requestedAt, &expiresAt)
	if err != nil {
		return Pairing{}, err
	}
	if p.RequestedAt, err = time.Parse(time.RFC3339, requestedAt); err != nil {
		return Pairing{}, err
	}
	if p.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt); err != nil {
		return Pairing{}, err
	}
	if p.Status == "pending" && time.Now().UTC().After(p.ExpiresAt) {
		p.Status = "expired"
	}
	return p, nil
}

// GetPairing returns the pairing, reporting stale pending rows as expired.
func (s *Store) GetPairing(ctx context.Context, pairingID string) (Pairing, bool, error) {
	p, err := scanPairing(s.db.QueryRowContext(ctx,
		`SELECT pairing_id, code, device_name, device_type, app_version, status, requested_at, expires_at
		 FROM pairings WHERE pairing_id = ?`, pairingID))
	if errors.Is(err, sql.ErrNoRows) {
		return Pairing{}, false, nil
	}
	if err != nil {
		return Pairing{}, false, err
	}
	return p, true, nil
}

// ListPendingPairings returns actionable (pending, unexpired) requests.
func (s *Store) ListPendingPairings(ctx context.Context) ([]Pairing, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pairing_id, code, device_name, device_type, app_version, status, requested_at, expires_at
		 FROM pairings WHERE status = 'pending' AND expires_at > ? ORDER BY requested_at`,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pairing
	for rows.Next() {
		var p Pairing
		var requestedAt, expiresAt string
		if err := rows.Scan(&p.PairingID, &p.Code, &p.DeviceName, &p.DeviceType, &p.AppVersion, &p.Status, &requestedAt, &expiresAt); err != nil {
			return nil, err
		}
		if p.RequestedAt, err = time.Parse(time.RFC3339, requestedAt); err != nil {
			return nil, err
		}
		if p.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// setPairingStatus flips a live pending pairing; anything else is ErrNotFound
// (expired/denied/approved pairings are not actionable).
func (s *Store) setPairingStatus(ctx context.Context, pairingID, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE pairings SET status = ? WHERE pairing_id = ? AND status = 'pending' AND expires_at > ?`,
		status, pairingID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("pairing %s: %w", pairingID, ErrNotFound)
	}
	return nil
}

func (s *Store) ApprovePairing(ctx context.Context, pairingID string) error {
	return s.setPairingStatus(ctx, pairingID, "approved")
}

func (s *Store) DenyPairing(ctx context.Context, pairingID string) error {
	return s.setPairingStatus(ctx, pairingID, "denied")
}

// DeliverPairingToken mints the device + device token for an approved pairing,
// exactly once (raw tokens are never stored, so minting happens at the first
// post-approval poll). ok=false when there is nothing to deliver.
func (s *Store) DeliverPairingToken(ctx context.Context, pairingID string) (token, deviceID string, ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", false, err
	}
	defer tx.Rollback()

	var name, typ string
	err = tx.QueryRowContext(ctx,
		`SELECT device_name, device_type FROM pairings
		 WHERE pairing_id = ? AND status = 'approved' AND device_id IS NULL`, pairingID).Scan(&name, &typ)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}

	deviceID = NewID("dev")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO devices (device_id, name, type) VALUES (?, ?, ?)`, deviceID, name, typ); err != nil {
		return "", "", false, err
	}
	raw := newRawToken()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO device_tokens (token_hash, device_id) VALUES (?, ?)`, HashToken(raw), deviceID); err != nil {
		return "", "", false, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE pairings SET device_id = ? WHERE pairing_id = ? AND device_id IS NULL`, deviceID, pairingID)
	if err != nil {
		return "", "", false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return "", "", false, err // raced: someone else delivered
	}
	if err := tx.Commit(); err != nil {
		return "", "", false, err
	}
	return raw, deviceID, true, nil
}

// CreatePairCode mints a single-use QR code.
func (s *Store) CreatePairCode(ctx context.Context) (string, time.Time, error) {
	code := newCode()
	now := time.Now().UTC()
	expiresAt := now.Add(pairCodeTTL)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pair_codes (code, created_at, expires_at) VALUES (?, ?, ?)`,
		code, now.Format(time.RFC3339), expiresAt.Format(time.RFC3339))
	if err != nil {
		return "", time.Time{}, err
	}
	return code, expiresAt, nil
}

// ClaimPairCode consumes a QR code and mints the device + device token in one
// transaction. Unknown code → ErrNotFound; used or expired → ErrGone.
func (s *Store) ClaimPairCode(ctx context.Context, code, deviceName, deviceType string) (token, deviceID string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(ctx,
		`UPDATE pair_codes SET used_at = ? WHERE code = ? AND used_at IS NULL AND expires_at > ?`,
		now, code, now)
	if err != nil {
		return "", "", err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", "", err
	} else if n == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM pair_codes WHERE code = ?`, code).Scan(&exists); err != nil {
			return "", "", err
		}
		if exists == 0 {
			return "", "", fmt.Errorf("pair code: %w", ErrNotFound)
		}
		return "", "", fmt.Errorf("pair code: %w", ErrGone)
	}

	deviceID = NewID("dev")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO devices (device_id, name, type) VALUES (?, ?, ?)`, deviceID, deviceName, deviceType); err != nil {
		return "", "", err
	}
	raw := newRawToken()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO device_tokens (token_hash, device_id) VALUES (?, ?)`, HashToken(raw), deviceID); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return raw, deviceID, nil
}

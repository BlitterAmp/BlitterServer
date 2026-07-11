package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/auth"
)

// ProfileRecord is the admin/app view of a household profile. PINs surface
// only as HasPin; the hash never leaves this package.
type ProfileRecord struct {
	ProfileID      string
	Name           string
	HasPin         bool
	AvatarColor    string
	ShareListening bool
}

// ProfileUpdate carries PATCH semantics: nil pointer = leave unchanged.
// SetPin=true applies Pin, where empty string clears it.
type ProfileUpdate struct {
	Name        *string
	AvatarColor *string
	SetPin      bool
	Pin         string
}

// DeviceRecord mirrors the contract's Device schema.
type DeviceRecord struct {
	DeviceID   string
	Name       string
	Type       string
	PairedAt   time.Time
	LastSeenAt *time.Time
}

// CreateProfileRecord creates a profile; pin="" means no PIN.
func (s *Store) CreateProfileRecord(ctx context.Context, name, pin, avatarColor string) (ProfileRecord, error) {
	var pinHash sql.NullString
	if pin != "" {
		h, err := auth.HashPassword(pin)
		if err != nil {
			return ProfileRecord{}, err
		}
		pinHash = sql.NullString{String: h, Valid: true}
	}
	var color sql.NullString
	if avatarColor != "" {
		color = sql.NullString{String: avatarColor, Valid: true}
	}
	id := NewID("prf")
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO profiles (profile_id, name, pin_hash, avatar_color) VALUES (?, ?, ?, ?)`,
		id, name, pinHash, color)
	if err != nil {
		return ProfileRecord{}, err
	}
	return ProfileRecord{ProfileID: id, Name: name, HasPin: pin != "", AvatarColor: avatarColor, ShareListening: true}, nil
}

func scanProfileRecord(scan func(dest ...any) error) (ProfileRecord, error) {
	var p ProfileRecord
	var pinHash, color sql.NullString
	var share int
	if err := scan(&p.ProfileID, &p.Name, &pinHash, &color, &share); err != nil {
		return ProfileRecord{}, err
	}
	p.HasPin = pinHash.Valid
	p.AvatarColor = color.String
	p.ShareListening = share != 0
	return p, nil
}

const profileCols = `profile_id, name, pin_hash, avatar_color, share_listening`

func (s *Store) GetProfileRecord(ctx context.Context, profileID string) (ProfileRecord, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+profileCols+` FROM profiles WHERE profile_id = ?`, profileID)
	p, err := scanProfileRecord(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ProfileRecord{}, false, nil
	}
	if err != nil {
		return ProfileRecord{}, false, err
	}
	return p, true, nil
}

func (s *Store) ListProfileRecords(ctx context.Context) ([]ProfileRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+profileCols+` FROM profiles ORDER BY created_at, name, profile_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfileRecord
	for rows.Next() {
		p, err := scanProfileRecord(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProfile applies the non-nil fields and returns the updated record.
func (s *Store) UpdateProfile(ctx context.Context, profileID string, u ProfileUpdate) (ProfileRecord, error) {
	if u.Name != nil {
		if _, err := s.exec1(ctx, `UPDATE profiles SET name = ? WHERE profile_id = ?`, *u.Name, profileID); err != nil {
			return ProfileRecord{}, err
		}
	}
	if u.AvatarColor != nil {
		var color sql.NullString
		if *u.AvatarColor != "" {
			color = sql.NullString{String: *u.AvatarColor, Valid: true}
		}
		if _, err := s.exec1(ctx, `UPDATE profiles SET avatar_color = ? WHERE profile_id = ?`, color, profileID); err != nil {
			return ProfileRecord{}, err
		}
	}
	if u.SetPin {
		var pinHash sql.NullString
		if u.Pin != "" {
			h, err := auth.HashPassword(u.Pin)
			if err != nil {
				return ProfileRecord{}, err
			}
			pinHash = sql.NullString{String: h, Valid: true}
		}
		if _, err := s.exec1(ctx, `UPDATE profiles SET pin_hash = ? WHERE profile_id = ?`, pinHash, profileID); err != nil {
			return ProfileRecord{}, err
		}
	}
	p, found, err := s.GetProfileRecord(ctx, profileID)
	if err != nil {
		return ProfileRecord{}, err
	}
	if !found {
		return ProfileRecord{}, fmt.Errorf("profile %s: %w", profileID, ErrNotFound)
	}
	return p, nil
}

// exec1 runs an update that must touch at least one row, else ErrNotFound.
func (s *Store) exec1(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	return n, nil
}

func (s *Store) DeleteProfile(ctx context.Context, profileID string) error {
	_, err := s.exec1(ctx, `DELETE FROM profiles WHERE profile_id = ?`, profileID)
	if err != nil {
		return fmt.Errorf("profile %s: %w", profileID, err)
	}
	return nil
}

// VerifyProfilePIN checks pin against the stored hash. Pinless profiles
// verify only with an empty pin.
func (s *Store) VerifyProfilePIN(ctx context.Context, profileID, pin string) (ok, hasPin bool, err error) {
	var pinHash sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT pin_hash FROM profiles WHERE profile_id = ?`, profileID).Scan(&pinHash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, fmt.Errorf("profile %s: %w", profileID, ErrNotFound)
	}
	if err != nil {
		return false, false, err
	}
	if !pinHash.Valid {
		return pin == "", false, nil
	}
	ok, err = auth.VerifyPassword(pin, pinHash.String)
	return ok, true, err
}

func (s *Store) SetShareListening(ctx context.Context, profileID string, share bool) error {
	v := 0
	if share {
		v = 1
	}
	_, err := s.exec1(ctx, `UPDATE profiles SET share_listening = ? WHERE profile_id = ?`, v, profileID)
	if err != nil {
		return fmt.Errorf("profile %s: %w", profileID, err)
	}
	return nil
}

func scanDevice(scan func(dest ...any) error) (DeviceRecord, error) {
	var d DeviceRecord
	var pairedAt string
	var lastSeen sql.NullString
	if err := scan(&d.DeviceID, &d.Name, &d.Type, &pairedAt, &lastSeen); err != nil {
		return DeviceRecord{}, err
	}
	t, err := parseSQLiteTime(pairedAt)
	if err != nil {
		return DeviceRecord{}, err
	}
	d.PairedAt = t
	if lastSeen.Valid {
		t, err := parseSQLiteTime(lastSeen.String)
		if err != nil {
			return DeviceRecord{}, err
		}
		d.LastSeenAt = &t
	}
	return d, nil
}

// parseSQLiteTime accepts both RFC3339 (Go-written) and SQLite's
// datetime('now') format (column defaults).
func parseSQLiteTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02 15:04:05", s)
}

func (s *Store) GetDevice(ctx context.Context, deviceID string) (DeviceRecord, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT device_id, name, type, paired_at, last_seen_at FROM devices WHERE device_id = ?`, deviceID)
	d, err := scanDevice(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceRecord{}, false, nil
	}
	if err != nil {
		return DeviceRecord{}, false, err
	}
	return d, true, nil
}

func (s *Store) ListDevices(ctx context.Context) ([]DeviceRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT device_id, name, type, paired_at, last_seen_at FROM devices ORDER BY paired_at, device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceRecord
	for rows.Next() {
		d, err := scanDevice(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TouchDevice records activity for the admin device list.
func (s *Store) TouchDevice(ctx context.Context, deviceID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET last_seen_at = ? WHERE device_id = ?`,
		time.Now().UTC().Format(time.RFC3339), deviceID)
	return err
}

// Counts feeds the admin dashboard: profiles, devices, actionable pairings.
func (s *Store) Counts(ctx context.Context) (profiles, devices, pendingPairings int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM profiles),
		       (SELECT count(*) FROM devices),
		       (SELECT count(*) FROM pairings WHERE status = 'pending' AND expires_at > ?)`,
		time.Now().UTC().Format(time.RFC3339)).Scan(&profiles, &devices, &pendingPairings)
	return
}

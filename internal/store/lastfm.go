package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// LastfmConnection contains PII and a secret. It must never be serialized
// outside the encrypted store payload or logged.
type LastfmConnection struct{ Username, SessionKey string }

func (s *Store) sealLastfm(profileID string, conn LastfmConnection) ([]byte, error) {
	plain, err := json.Marshal(conn)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, s.secret.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.secret.Seal(nonce, nonce, plain, []byte(profileID)), nil
}

func (s *Store) openLastfm(profileID string, sealed []byte) (LastfmConnection, error) {
	var out LastfmConnection
	if len(sealed) < s.secret.NonceSize() {
		return out, errors.New("invalid encrypted last.fm connection")
	}
	plain, err := s.secret.Open(nil, sealed[:s.secret.NonceSize()], sealed[s.secret.NonceSize():], []byte(profileID))
	if err != nil {
		return out, fmt.Errorf("decrypt last.fm connection: %w", err)
	}
	if err := json.Unmarshal(plain, &out); err != nil || out.Username == "" || out.SessionKey == "" {
		return LastfmConnection{}, errors.New("invalid encrypted last.fm connection")
	}
	return out, nil
}

func (s *Store) CreateLastfmAttempt(ctx context.Context, profileID, state string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM lastfm_auth_attempts WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO lastfm_auth_attempts(state,profile_id,expires_at) VALUES(?,?,?)`, state, profileID, expires.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ClaimLastfmAttempt(ctx context.Context, state, claimID string) (string, error) {
	now := time.Now().UTC()
	stale := now.Add(-time.Minute).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `UPDATE lastfm_auth_attempts SET claim_id=?,claimed_at=? WHERE state=? AND expires_at>? AND (claim_id IS NULL OR claimed_at<?)`, claimID, now.Format(time.RFC3339), state, now.Format(time.RFC3339), stale)
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return "", ErrGone
	}
	var profileID string
	if err := s.db.QueryRowContext(ctx, `SELECT profile_id FROM lastfm_auth_attempts WHERE state=? AND claim_id=?`, state, claimID).Scan(&profileID); err != nil {
		return "", err
	}
	return profileID, nil
}

func (s *Store) ReleaseLastfmAttempt(ctx context.Context, state, claimID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_auth_attempts SET claim_id=NULL,claimed_at=NULL WHERE state=? AND claim_id=?`, state, claimID)
	return err
}

func (s *Store) CompleteLastfmAttempt(ctx context.Context, state, claimID, profileID string, conn LastfmConnection) error {
	sealed, err := s.sealLastfm(profileID, conn)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM lastfm_auth_attempts WHERE state=? AND profile_id=? AND claim_id=?`, state, profileID, claimID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrGone
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO lastfm_profiles(profile_id,encrypted_payload,connected_at) VALUES(?,?,?) ON CONFLICT(profile_id) DO UPDATE SET encrypted_payload=excluded.encrypted_payload,connected_at=excluded.connected_at`, profileID, sealed, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetLastfmConnection(ctx context.Context, profileID, username, sessionKey string) error {
	sealed, err := s.sealLastfm(profileID, LastfmConnection{Username: username, SessionKey: sessionKey})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO lastfm_profiles(profile_id,encrypted_payload,connected_at) VALUES(?,?,?) ON CONFLICT(profile_id) DO UPDATE SET encrypted_payload=excluded.encrypted_payload,connected_at=excluded.connected_at`, profileID, sealed, time.Now().UTC().Format(time.RFC3339))
	return err
}
func (s *Store) GetLastfmConnection(ctx context.Context, profileID string) (LastfmConnection, bool, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT encrypted_payload FROM lastfm_profiles WHERE profile_id=?`, profileID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return LastfmConnection{}, false, nil
	}
	if err != nil {
		return LastfmConnection{}, false, err
	}
	out, err := s.openLastfm(profileID, sealed)
	return out, err == nil, err
}

func (s *Store) LastfmDataCategories(ctx context.Context, profileID string) ([]string, error) {
	var connected, attempts, playback int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lastfm_profiles WHERE profile_id=?), EXISTS(SELECT 1 FROM lastfm_auth_attempts WHERE profile_id=?), EXISTS(SELECT 1 FROM lastfm_play_sessions WHERE profile_id=? UNION ALL SELECT 1 FROM playback_events WHERE profile_id=? AND play_session_id IS NOT NULL)`, profileID, profileID, profileID, profileID).Scan(&connected, &attempts, &playback); err != nil {
		return nil, err
	}
	var out []string
	if connected != 0 {
		out = append(out, "lastfm_username", "lastfm_session")
	}
	if attempts != 0 {
		out = append(out, "lastfm_auth_attempts")
	}
	if playback != 0 {
		out = append(out, "lastfm_playback")
	}
	return out, nil
}
func (s *Store) DeleteLastfmData(ctx context.Context, profileID string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	cats, err := lastfmCategoriesTx(ctx, tx, profileID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM lastfm_auth_attempts WHERE profile_id=?`, profileID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM lastfm_profiles WHERE profile_id=?`, profileID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE playback_events SET play_session_id=NULL WHERE profile_id=? AND play_session_id IS NOT NULL`, profileID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM lastfm_play_sessions WHERE profile_id=?`, profileID); err != nil {
		return nil, err
	}
	return cats, tx.Commit()
}
func lastfmCategoriesTx(ctx context.Context, tx *sql.Tx, profileID string) ([]string, error) {
	var connected, attempts, playback int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lastfm_profiles WHERE profile_id=?), EXISTS(SELECT 1 FROM lastfm_auth_attempts WHERE profile_id=?), EXISTS(SELECT 1 FROM lastfm_play_sessions WHERE profile_id=? UNION ALL SELECT 1 FROM playback_events WHERE profile_id=? AND play_session_id IS NOT NULL)`, profileID, profileID, profileID, profileID).Scan(&connected, &attempts, &playback); err != nil {
		return nil, err
	}
	var out []string
	if connected != 0 {
		out = append(out, "lastfm_username", "lastfm_session")
	}
	if attempts != 0 {
		out = append(out, "lastfm_auth_attempts")
	}
	if playback != 0 {
		out = append(out, "lastfm_playback")
	}
	return out, nil
}
func (s *Store) CountLastfmProfiles(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM lastfm_profiles`).Scan(&n)
	return n, err
}
func (s *Store) DeleteAllLastfmData(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM lastfm_auth_attempts`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM lastfm_profiles`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE playback_events SET play_session_id=NULL WHERE play_session_id IS NOT NULL`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM lastfm_play_sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

// LastfmPlayback is sensitive behavioral data and must not be logged.
type LastfmPlayback struct {
	SessionID, ProfileID, Artist, Title, Album string
	PositionSec                                float64
	StartedAt                                  time.Time
	DurationMs                                 int
}

func (s *Store) FinishLastfmPlay(ctx context.Context, sessionID, claimID, state, reason string) error {
	now := time.Now().UTC()
	var next any
	if state == "pending" {
		var attempts int
		_ = s.db.QueryRowContext(ctx, `SELECT attempt_count FROM lastfm_play_sessions WHERE play_session_id=?`, sessionID).Scan(&attempts)
		delay := time.Minute * time.Duration(1<<min(attempts, 6))
		next = now.Add(delay).Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_play_sessions SET relay_state=?,terminal_reason=?,claim_id=NULL,claimed_at=NULL,attempt_count=attempt_count+1,next_attempt_at=?,updated_at=? WHERE play_session_id=? AND claim_id=?`, state, reason, next, now.Format(time.RFC3339), sessionID, claimID)
	return err
}

func (s *Store) ClaimPendingLastfmPlays(ctx context.Context, now time.Time, claimID string, limit int) ([]LastfmPlayback, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ps.play_session_id FROM lastfm_play_sessions ps JOIN tracks t ON t.track_id=ps.track_id JOIN lastfm_profiles lp ON lp.profile_id=ps.profile_id WHERE ((ps.relay_state='pending' AND (ps.next_attempt_at IS NULL OR ps.next_attempt_at<=?)) OR (ps.relay_state='claimed' AND ps.claimed_at<?)) AND t.duration_ms>30000 AND ps.position_sec>=min(240.0,t.duration_ms/2000.0) ORDER BY ps.started_at LIMIT ?`, now.Format(time.RFC3339), now.Add(-5*time.Minute).Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	var out []LastfmPlayback
	for _, sid := range ids {
		res, err := s.db.ExecContext(ctx, `UPDATE lastfm_play_sessions SET relay_state='claimed',claim_id=?,claimed_at=?,updated_at=? WHERE play_session_id=? AND ((relay_state='pending' AND (next_attempt_at IS NULL OR next_attempt_at<=?)) OR (relay_state='claimed' AND claimed_at<?))`, claimID, now.Format(time.RFC3339), now.Format(time.RFC3339), sid, now.Format(time.RFC3339), now.Add(-5*time.Minute).Format(time.RFC3339))
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			continue
		}
		p, err := s.lastfmPlayback(ctx, sid)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
func (s *Store) lastfmPlayback(ctx context.Context, sid string) (LastfmPlayback, error) {
	var p LastfmPlayback
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT ps.play_session_id,ps.profile_id,t.artist_name,t.title,a.title,ps.position_sec,ps.started_at,t.duration_ms FROM lastfm_play_sessions ps JOIN tracks t ON t.track_id=ps.track_id JOIN albums a ON a.album_id=t.album_id WHERE ps.play_session_id=?`, sid).Scan(&p.SessionID, &p.ProfileID, &p.Artist, &p.Title, &p.Album, &p.PositionSec, &at, &p.DurationMs)
	if err == nil {
		p.StartedAt, _ = time.Parse(time.RFC3339, at)
	}
	return p, err
}

// ClaimPendingNowPlaying marks attempts before I/O. Last.fm explicitly says
// failures must not be retried, so attempted is terminal for dispatch.
func (s *Store) ClaimPendingNowPlaying(ctx context.Context, limit int) ([]LastfmPlayback, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ps.play_session_id FROM lastfm_play_sessions ps JOIN lastfm_profiles lp ON lp.profile_id=ps.profile_id WHERE ps.now_playing_state='pending' ORDER BY ps.started_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	var out []LastfmPlayback
	for _, sid := range ids {
		res, err := s.db.ExecContext(ctx, `UPDATE lastfm_play_sessions SET now_playing_state='attempted',updated_at=? WHERE play_session_id=? AND now_playing_state='pending'`, time.Now().UTC().Format(time.RFC3339), sid)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			p, err := s.lastfmPlayback(ctx, sid)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	return out, nil
}

const LastfmTerminalRetention = 30 * 24 * time.Hour
const LastfmPendingRetention = 7 * 24 * time.Hour

func (s *Store) CleanupLastfmPlaySessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM lastfm_play_sessions WHERE (relay_state='terminal' AND updated_at<?) OR (relay_state IN ('pending','claimed') AND started_at<?)`, now.Add(-LastfmTerminalRetention).Format(time.RFC3339), now.Add(-LastfmPendingRetention).Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) FinishNowPlaying(ctx context.Context, sessionID string, delivered bool) error {
	outcome := "failed"
	if delivered {
		outcome = "delivered"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_play_sessions SET now_playing_outcome=? WHERE play_session_id=? AND now_playing_state='attempted'`, outcome, sessionID)
	return err
}
func (s *Store) LastfmNowPlayingStatus(ctx context.Context, sessionID string) (state, outcome string, err error) {
	var o sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT now_playing_state,now_playing_outcome FROM lastfm_play_sessions WHERE play_session_id=?`, sessionID).Scan(&state, &o)
	if o.Valid {
		outcome = o.String
	}
	return
}

func (s *Store) ResolveArtistName(ctx context.Context, name string) (id string, owned bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE name=? AND missing=0`, name).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	candidate := NewID("art")
	_, err = s.db.ExecContext(ctx, `INSERT INTO external_artists(artist_id,name,created_at) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, candidate, name, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", false, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT artist_id FROM external_artists WHERE name=?`, name).Scan(&id)
	return id, false, err
}

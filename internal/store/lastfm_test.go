package store

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLastfmConnectionEncryptedAndErased(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p, err := s.CreateProfileRecord(ctx, "Synthetic", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastfmConnection(ctx, p.ProfileID, "synthetic_user", "synthetic_session_secret"); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.db.QueryRow(`SELECT encrypted_payload FROM lastfm_profiles WHERE profile_id=?`, p.ProfileID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic_session_secret") {
		t.Fatal("session key stored in plaintext")
	}
	if strings.Contains(string(raw), "synthetic_user") {
		t.Fatal("username stored in plaintext")
	}
	got, ok, err := s.GetLastfmConnection(ctx, p.ProfileID)
	if err != nil || !ok || got.Username != "synthetic_user" || got.SessionKey != "synthetic_session_secret" {
		t.Fatalf("round trip: %#v %v %v", got, ok, err)
	}
	if _, err := s.DeleteLastfmData(ctx, p.ProfileID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetLastfmConnection(ctx, p.ProfileID); ok {
		t.Fatal("connection retained after erasure")
	}
}

func TestLastfmErasureCategoriesAndProfileCascade(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p, _ := s.CreateProfileRecord(ctx, "Synthetic", "", "")
	_ = s.CreateLastfmAttempt(ctx, p.ProfileID, "attempt", time.Now().Add(time.Minute))
	cats, _ := s.LastfmDataCategories(ctx, p.ProfileID)
	if strings.Join(cats, ",") != "lastfm_auth_attempts" {
		t.Fatalf("attempt categories: %v", cats)
	}
	deleted, err := s.DeleteLastfmData(ctx, p.ProfileID)
	if err != nil || strings.Join(deleted, ",") != "lastfm_auth_attempts" {
		t.Fatalf("delete: %v %v", deleted, err)
	}
	deleted, err = s.DeleteLastfmData(ctx, p.ProfileID)
	if err != nil || len(deleted) != 0 {
		t.Fatalf("repeat: %v %v", deleted, err)
	}
	_ = s.SetLastfmConnection(ctx, p.ProfileID, "synthetic_user", "synthetic_key")
	cats, _ = s.LastfmDataCategories(ctx, p.ProfileID)
	if strings.Join(cats, ",") != "lastfm_username,lastfm_session" {
		t.Fatalf("connected categories: %v", cats)
	}
	if err := s.DeleteProfile(ctx, p.ProfileID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountLastfmProfiles(ctx); n != 0 {
		t.Fatal("profile deletion retained last.fm data")
	}
}

func TestLastfmErasureIncludesPlaybackAndRepeatedResultIsEmpty(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	p, _ := s.CreateProfileRecord(ctx, "Synthetic Erasure", "", "")
	_ = s.SetLastfmConnection(ctx, p.ProfileID, "synthetic_user", "synthetic_session")
	tracks, _, _ := s.ListTracks(ctx, "title", "", 1)
	at := time.Now().UTC()
	_, err := s.IngestPlaybackEvents(ctx, p.ProfileID, "dev_synthetic", []PlaybackEventRecord{{EventID: "start-sensitive", Type: "started", TrackID: tracks[0].TrackID, At: at}})
	if err != nil {
		t.Fatal(err)
	}
	cats, err := s.LastfmDataCategories(ctx, p.ProfileID)
	if err != nil || strings.Join(cats, ",") != "lastfm_username,lastfm_session,lastfm_playback" {
		t.Fatalf("playback dry-run: %v %v", cats, err)
	}
	deleted, err := s.DeleteLastfmData(ctx, p.ProfileID)
	if err != nil || strings.Join(deleted, ",") != "lastfm_username,lastfm_session,lastfm_playback" {
		t.Fatalf("playback delete: %v %v", deleted, err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM lastfm_play_sessions WHERE profile_id=?`, p.ProfileID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("playback retained: %d %v", count, err)
	}
	var events, linked int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*),count(play_session_id) FROM playback_events WHERE profile_id=?`, p.ProfileID).Scan(&events, &linked)
	if events != 1 || linked != 0 {
		t.Fatalf("local playback/linkage after erasure: events=%d linked=%d", events, linked)
	}
	again, err := s.DeleteLastfmData(ctx, p.ProfileID)
	if err != nil || len(again) != 0 {
		t.Fatalf("repeat: %v %v", again, err)
	}
}

func TestDeleteAllLastfmDataClearsLinksButRetainsPlayback(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	tracks, _, _ := s.ListTracks(ctx, "title", "", 1)
	for i := 0; i < 2; i++ {
		p, _ := s.CreateProfileRecord(ctx, fmt.Sprintf("Synthetic %d", i), "", "")
		_ = s.SetLastfmConnection(ctx, p.ProfileID, "synthetic_user", "synthetic_session")
		_, _ = s.IngestPlaybackEvents(ctx, p.ProfileID, "dev", []PlaybackEventRecord{{EventID: fmt.Sprintf("event-%d", i), Type: "started", TrackID: tracks[0].TrackID, At: time.Now().UTC()}})
	}
	if err := s.DeleteAllLastfmData(ctx); err != nil {
		t.Fatal(err)
	}
	var events, linked, sessions int
	_ = s.db.QueryRow(`SELECT count(*),count(play_session_id) FROM playback_events`).Scan(&events, &linked)
	_ = s.db.QueryRow(`SELECT count(*) FROM lastfm_play_sessions`).Scan(&sessions)
	if events != 2 || linked != 0 || sessions != 0 {
		t.Fatalf("global erase: events=%d linked=%d sessions=%d", events, linked, sessions)
	}
}

func TestLocalKeyLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server-local.key")
	const workers = 8
	keys := make([][]byte, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func() {
			defer wg.Done()
			key, err := loadOrCreateLocalKey(path)
			if err != nil {
				t.Errorf("key: %v", err)
				return
			}
			keys[i] = key
		}()
	}
	wg.Wait()
	for i := 1; i < workers; i++ {
		if !bytes.Equal(keys[0], keys[i]) {
			t.Fatal("concurrent key mismatch")
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", info.Mode().Perm(), err)
	}
	if err := os.WriteFile(path, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateLocalKey(path); err == nil {
		t.Fatal("malformed key accepted")
	}
}

func TestReplacedKeyCannotDecrypt(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProfileRecord(ctx, "Synthetic", "", "")
	_ = s.SetLastfmConnection(ctx, p.ProfileID, "synthetic_user", "synthetic_key")
	_ = s.Close()
	replacement := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(filepath.Join(dir, "server-local.key"), replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err := s.GetLastfmConnection(ctx, p.ProfileID); err == nil {
		t.Fatal("replaced key decrypted payload")
	}
}

func TestLastfmPlaySessionRetention(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	p, _ := s.CreateProfileRecord(ctx, "Synthetic Retention", "", "")
	_ = s.SetLastfmConnection(ctx, p.ProfileID, "synthetic_user", "synthetic_session")
	tracks, _, _ := s.ListTracks(ctx, "title", "", 1)
	now := time.Now().UTC()
	for _, tc := range []struct {
		id, state string
		age       time.Duration
	}{{"old-terminal", "terminal", LastfmTerminalRetention + time.Hour}, {"fresh-terminal", "terminal", time.Hour}, {"old-pending", "pending", LastfmPendingRetention + time.Hour}, {"fresh-pending", "pending", time.Hour}} {
		at := now.Add(-tc.age).Format(time.RFC3339)
		_, err := s.db.ExecContext(ctx, `INSERT INTO lastfm_play_sessions(play_session_id,profile_id,track_id,started_at,relay_state,updated_at) VALUES(?,?,?,?,?,?)`, tc.id, p.ProfileID, tracks[0].TrackID, at, tc.state, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.CleanupLastfmPlaySessions(ctx, now)
	if err != nil || n != 2 {
		t.Fatalf("cleanup=%d %v", n, err)
	}
	var remain int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM lastfm_play_sessions`).Scan(&remain)
	if remain != 2 {
		t.Fatalf("remaining=%d", remain)
	}
}

func TestLastfmAttemptSingleUseAndExpiry(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p, _ := s.CreateProfileRecord(ctx, "Synthetic", "", "")
	_ = s.CreateLastfmAttempt(ctx, p.ProfileID, "fresh", time.Now().Add(time.Minute))
	if got, err := s.ClaimLastfmAttempt(ctx, "fresh", "claim"); err != nil || got != p.ProfileID {
		t.Fatalf("consume: %q %v", got, err)
	}
	if _, err := s.ClaimLastfmAttempt(ctx, "fresh", "other"); err != ErrGone {
		t.Fatalf("reuse: %v", err)
	}
	_ = s.CreateLastfmAttempt(ctx, p.ProfileID, "expired", time.Now().Add(-time.Minute))
	if _, err := s.ClaimLastfmAttempt(ctx, "expired", "claim"); err != ErrGone {
		t.Fatalf("expiry: %v", err)
	}
}

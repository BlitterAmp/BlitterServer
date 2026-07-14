package server

import (
	"context"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func TestAdminDiscogsCredentialLifecycle(t *testing.T) {
	s, st, _, _, _ := dataSrv(t)
	ctx := context.Background()

	get, err := s.AdminGetDiscogs(ctx, api.AdminGetDiscogsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if get.(api.AdminGetDiscogs200JSONResponse).Configured {
		t.Fatal("unconfigured Discogs reported configured")
	}

	empty := " \t\n "
	if _, err := s.AdminSetDiscogs(ctx, api.AdminSetDiscogsRequestObject{Body: &api.AdminSetDiscogsJSONRequestBody{PersonalToken: &empty}}); err == nil {
		t.Fatal("trimmed-empty personal token accepted")
	}
	if _, ok, err := st.GetSetting(ctx, "discogs_personal_token"); err != nil || ok {
		t.Fatalf("invalid token persisted: ok=%v err=%v", ok, err)
	}

	token := "  personal-token  "
	if _, err := s.AdminSetDiscogs(ctx, api.AdminSetDiscogsRequestObject{Body: &api.AdminSetDiscogsJSONRequestBody{PersonalToken: &token}}); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := st.GetSetting(ctx, "discogs_personal_token"); err != nil || !ok || got != "personal-token" {
		t.Fatalf("stored token=%q ok=%v err=%v", got, ok, err)
	}
	get, err = s.AdminGetDiscogs(ctx, api.AdminGetDiscogsRequestObject{})
	if err != nil || !get.(api.AdminGetDiscogs200JSONResponse).Configured {
		t.Fatalf("configured state response=%#v err=%v", get, err)
	}

	if _, err := s.AdminDeleteDiscogs(ctx, api.AdminDeleteDiscogsRequestObject{}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := st.GetSetting(ctx, "discogs_personal_token"); err != nil || got != "" {
		t.Fatalf("deleted token=%q err=%v", got, err)
	}
}

func TestAdminSetDiscogsResetsAlbumAndArtistEnrichmentAsynchronously(t *testing.T) {
	s, st, _, _, _ := dataSrv(t)
	ctx := context.Background()
	now := time.Now()
	albums, err := st.AlbumsNeedingArtAt(ctx, now, 100)
	if err != nil || len(albums) == 0 {
		t.Fatalf("albums needing art=%d err=%v", len(albums), err)
	}
	for _, album := range albums {
		if err := st.MarkAlbumArtAttempt(ctx, album.AlbumID, store.ArtAttemptMiss, now); err != nil {
			t.Fatal(err)
		}
	}
	artists, err := st.ArtistsNeedingArtAt(ctx, now, 100)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists needing art=%d err=%v", len(artists), err)
	}
	for _, artist := range artists {
		if err := st.MarkArtistArtAttempt(ctx, artist.ArtistID, store.ArtAttemptMiss, now); err != nil {
			t.Fatal(err)
		}
	}

	e := &handlerEnricher{started: make(chan struct{}, 3), release: make(chan struct{}, 3)}
	s.lib.SetEnricher(e)
	assertTriggered(t, e.started)
	token := "token"
	result := make(chan error, 1)
	go func() {
		_, err := s.AdminSetDiscogs(ctx, api.AdminSetDiscogsRequestObject{Body: &api.AdminSetDiscogsJSONRequestBody{PersonalToken: &token}})
		result <- err
	}()
	waitHandlerResult(t, result)
	e.release <- struct{}{}
	assertTriggered(t, e.started)

	if due, err := st.AlbumsNeedingArtAt(ctx, now, 100); err != nil || len(due) != len(albums) {
		t.Fatalf("album retries not reset: due=%d want=%d err=%v", len(due), len(albums), err)
	}
	if due, err := st.ArtistsNeedingArtAt(ctx, now, 100); err != nil || len(due) != len(artists) {
		t.Fatalf("artist retries not reset: due=%d want=%d err=%v", len(due), len(artists), err)
	}
	e.release <- struct{}{}
}

func TestAdminArtProviderCredentialsResetAlbumAndArtistRetries(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Server) error
	}{
		{
			name: "lastfm",
			set: func(s *Server) error {
				_, err := s.AdminSetLastfm(context.Background(), api.AdminSetLastfmRequestObject{Body: &api.AdminSetLastfmJSONRequestBody{ApiKey: "key", SharedSecret: "secret"}})
				return err
			},
		},
		{
			name: "fanart",
			set: func(s *Server) error {
				_, err := s.AdminSetFanart(context.Background(), api.AdminSetFanartRequestObject{Body: &api.AdminSetFanartJSONRequestBody{ApiKey: "key"}})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, st, _, _, _ := dataSrv(t)
			ctx := context.Background()
			now := time.Now()
			albums, err := st.AlbumsNeedingArtAt(ctx, now, 100)
			if err != nil || len(albums) == 0 {
				t.Fatalf("albums needing art=%d err=%v", len(albums), err)
			}
			for _, album := range albums {
				if err := st.MarkAlbumArtAttempt(ctx, album.AlbumID, store.ArtAttemptMiss, now); err != nil {
					t.Fatal(err)
				}
			}
			artists, err := st.ArtistsNeedingArtAt(ctx, now, 100)
			if err != nil || len(artists) == 0 {
				t.Fatalf("artists needing art=%d err=%v", len(artists), err)
			}
			for _, artist := range artists {
				if err := st.MarkArtistArtAttempt(ctx, artist.ArtistID, store.ArtAttemptMiss, now); err != nil {
					t.Fatal(err)
				}
			}

			e := &handlerEnricher{started: make(chan struct{}, 3), release: make(chan struct{}, 3)}
			s.lib.SetEnricher(e)
			assertTriggered(t, e.started)
			result := make(chan error, 1)
			go func() { result <- tt.set(s) }()
			waitHandlerResult(t, result)
			e.release <- struct{}{}
			assertTriggered(t, e.started)

			if due, err := st.AlbumsNeedingArtAt(ctx, now, 100); err != nil || len(due) != len(albums) {
				t.Fatalf("album retries not reset: due=%d want=%d err=%v", len(due), len(albums), err)
			}
			if due, err := st.ArtistsNeedingArtAt(ctx, now, 100); err != nil || len(due) != len(artists) {
				t.Fatalf("artist retries not reset: due=%d want=%d err=%v", len(due), len(artists), err)
			}
			e.release <- struct{}{}
		})
	}
}

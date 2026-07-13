package party

import (
	"context"
	"errors"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func fixture(t *testing.T) (*Manager, *store.Store, string, string, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	host, _ := st.CreateProfileRecord(ctx, "Host", "", "")
	guest, _ := st.CreateProfileRecord(ctx, "Guest", "", "")

	seq, _ := st.NextScanSeq(ctx)
	st.UpsertTrack(ctx, "filesystem", trackMeta("a/1.flac", "One"), "", seq)
	st.FinishScan(ctx, "filesystem", seq)
	tracks, _, _ := st.ListTracks(ctx, "title", "", 1)

	return NewManager(st, events.NewBus(st)), st, host.ProfileID, guest.ProfileID, tracks[0].TrackID
}

func TestPartyLifecycle(t *testing.T) {
	m, _, host, guest, trackID := fixture(t)
	ctx := context.Background()

	p, err := m.Create(ctx, host, "Kitchen Disco")
	if err != nil || p.HostProfileID != host || len(p.Members) != 1 {
		t.Fatalf("create: %v %+v", err, p)
	}

	// Guest can't see it before invite/join; host lists it.
	if visible := m.ListFor(ctx, guest); len(visible) != 0 {
		t.Fatalf("uninvited visibility: %+v", visible)
	}
	if visible := m.ListFor(ctx, host); len(visible) != 1 {
		t.Fatalf("host visibility: %+v", visible)
	}

	if err := m.Invite(ctx, p.PartyID, host, []string{guest}); err != nil {
		t.Fatal(err)
	}
	if visible := m.ListFor(ctx, guest); len(visible) != 1 {
		t.Fatalf("invited visibility: %+v", visible)
	}
	joined, err := m.Join(ctx, p.PartyID, guest)
	if err != nil || len(joined.Members) != 2 {
		t.Fatalf("join: %v %+v", err, joined)
	}

	// Any member queues; the first queued track starts the timeline.
	if err := m.AppendQueue(ctx, p.PartyID, guest, []string{trackID}); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(ctx, p.PartyID, guest)
	if err != nil || len(got.Queue) != 1 || got.Queue[0].Track.TrackID != trackID {
		t.Fatalf("queue: %v %+v", err, got)
	}

	// Transport is host-only.
	if _, err := m.Transport(ctx, p.PartyID, guest, "play", 0); !errors.Is(err, ErrHostOnly) {
		t.Fatalf("guest transport: %v", err)
	}
	state, err := m.Transport(ctx, p.PartyID, host, "play", 0)
	if err != nil || state.Paused || state.TrackID != trackID {
		t.Fatalf("play: %v %+v", err, state)
	}
	state, err = m.Transport(ctx, p.PartyID, host, "seek", 5000)
	if err != nil || state.PositionMs != 5000 {
		t.Fatalf("seek: %v %+v", err, state)
	}
	state, err = m.Transport(ctx, p.PartyID, host, "pause", 0)
	if err != nil || !state.Paused {
		t.Fatalf("pause: %v %+v", err, state)
	}

	// Kick is host-only and removes the member.
	if err := m.Kick(ctx, p.PartyID, guest, guest); !errors.Is(err, ErrHostOnly) {
		t.Fatalf("guest kick: %v", err)
	}
	if err := m.Kick(ctx, p.PartyID, host, guest); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, p.PartyID, guest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("kicked member access: %v", err)
	}

	// Host leaving ends the party.
	if err := m.Leave(ctx, p.PartyID, host); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, p.PartyID, host); !errors.Is(err, ErrNotFound) {
		t.Fatal("party must end when the host leaves")
	}
}

func TestPartyEndHostOnly(t *testing.T) {
	m, _, host, guest, _ := fixture(t)
	ctx := context.Background()
	p, _ := m.Create(ctx, host, "")
	m.Invite(ctx, p.PartyID, host, []string{guest})
	m.Join(ctx, p.PartyID, guest)

	if err := m.End(ctx, p.PartyID, guest); !errors.Is(err, ErrHostOnly) {
		t.Fatalf("guest end: %v", err)
	}
	if err := m.End(ctx, p.PartyID, host); err != nil {
		t.Fatal(err)
	}
	if err := m.End(ctx, p.PartyID, host); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double end: %v", err)
	}
}

func TestPartySkipAdvancesQueue(t *testing.T) {
	m, st, host, _, trackID := fixture(t)
	ctx := context.Background()
	seq, _ := st.NextScanSeq(ctx)
	st.UpsertTrack(ctx, "filesystem", trackMeta("a/1.flac", "One"), "", seq)
	st.UpsertTrack(ctx, "filesystem", trackMeta("a/2.flac", "Two"), "", seq)
	st.FinishScan(ctx, "filesystem", seq)
	tracks, _, _ := st.ListTracks(ctx, "title", "", 10)

	p, _ := m.Create(ctx, host, "")
	ids := []string{}
	for _, tr := range tracks {
		ids = append(ids, tr.TrackID)
	}
	if err := m.AppendQueue(ctx, p.PartyID, host, ids); err != nil {
		t.Fatal(err)
	}
	state, _ := m.Transport(ctx, p.PartyID, host, "play", 0)
	first := state.TrackID
	state, err := m.Transport(ctx, p.PartyID, host, "skip", 0)
	if err != nil || state.TrackID == first || state.TrackID == "" {
		t.Fatalf("skip: %v %+v", err, state)
	}
	_ = trackID
	// Skipping past the end stops playback.
	state, _ = m.Transport(ctx, p.PartyID, host, "skip", 0)
	if state.TrackID != "" || state.Paused != true {
		t.Fatalf("end of queue: %+v", state)
	}
}

func trackMeta(native, title string) source.TrackMeta {
	return source.TrackMeta{
		NativeID: native, Title: title, PrimaryArtist: source.ArtistReference{Name: "A"}, TrackCredits: []source.ArtistCredit{{Name: "A"}}, AlbumCredits: []source.ArtistCredit{{Name: "A"}}, Album: "Al",
		Genre: "Rock", DurationMs: 2000, Container: "flac", Codec: "flac", SizeBytes: 10, Version: 1,
	}
}

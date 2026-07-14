package filesystem

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

// fixtureLibrary builds a small on-disk library:
//
//	Artist One/Album A/01 - Sine Song.flac  (embedded cover art)
//	Artist One/Album A/02 - Sine Song.mp3
//	Artist Two/Album B/01 - Sine Song.m4a
//	Artist Two/Album B/notes.txt            (ignored)
func fixtureLibrary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH; fixture tests skipped")
	}
	root := t.TempDir()
	cover := filepath.Join(root, "cover.png")
	run(t, "ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=red:size=64x64:duration=1", "-frames:v", "1", cover)

	a1 := filepath.Join(root, "Artist One", "Album A")
	a2 := filepath.Join(root, "Artist Two", "Album B")
	os.MkdirAll(a1, 0o755)
	os.MkdirAll(a2, 0o755)

	run(t, "ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-i", cover,
		"-map", "0:a", "-map", "1:v", "-disposition:v", "attached_pic",
		"-metadata", "title=Sine Song", "-metadata", "artist=Artist One",
		"-metadata", "album=Album A", "-metadata", "genre=Rock",
		"-metadata", "musicbrainz_artistid=11111111-1111-1111-1111-111111111111",
		"-metadata", "musicbrainz_albumartistid=11111111-1111-1111-1111-111111111111",
		"-metadata", "musicbrainz_recordingid=22222222-2222-2222-2222-222222222222",
		"-metadata", "musicbrainz_albumid=33333333-3333-3333-3333-333333333333",
		"-metadata", "musicbrainz_releasegroupid=44444444-4444-4444-4444-444444444444",
		"-metadata", "date=1994", "-metadata", "track=1",
		filepath.Join(a1, "01 - Sine Song.flac"))
	run(t, "ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=550:duration=2",
		"-metadata", "title=Second Song", "-metadata", "artist=Artist One",
		"-metadata", "album=Album A", "-metadata", "genre=Rock",
		"-metadata", "date=1994", "-metadata", "track=2", "-b:a", "192k",
		filepath.Join(a1, "02 - Second Song.mp3"))
	run(t, "ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=660:duration=2",
		"-metadata", "title=Other Song", "-metadata", "artist=Artist Two",
		"-metadata", "album=Album B", "-metadata", "genre=Jazz",
		"-metadata", "date=2003", "-metadata", "track=1", "-c:a", "aac",
		filepath.Join(a2, "01 - Other Song.m4a"))
	os.WriteFile(filepath.Join(a2, "notes.txt"), []byte("not audio"), 0o644)
	os.Remove(cover)
	return root
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if b, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, b)
	}
}

func scanAll(t *testing.T, src *Source) []source.TrackMeta {
	t.Helper()
	var out []source.TrackMeta
	if err := src.Enumerate(context.Background(), func(candidate source.TrackCandidate) error {
		m, err := src.Parse(context.Background(), candidate)
		if err != nil {
			return err
		}
		out = append(out, m)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NativeID < out[j].NativeID })
	return out
}

func TestEnumerateCandidatesReportsFingerprintWithoutParsingMedia(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unparseable.mp3")
	contents := []byte("not an mp3, but enumeration must not care")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	wantTime := time.Unix(1_700_000_000, 123456789)
	if err := os.Chtimes(path, wantTime, wantTime); err != nil {
		t.Fatal(err)
	}
	src, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	var candidates []source.TrackCandidate
	if err := src.Enumerate(context.Background(), func(candidate source.TrackCandidate) error {
		candidates = append(candidates, candidate)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates=%+v", candidates)
	}
	got := candidates[0]
	if got.NativeID != "unparseable.mp3" || got.SizeBytes != int64(len(contents)) || got.MtimeNS != wantTime.UnixNano() {
		t.Fatalf("candidate=%+v", got)
	}
	if src.ParserVersion() <= 0 {
		t.Fatalf("parser version=%d", src.ParserVersion())
	}
}

func TestParseRejectsCandidateChangedBeforeRead(t *testing.T) {
	root := fixtureLibrary(t)
	src, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	var candidate source.TrackCandidate
	stop := errors.New("stop")
	err = src.Enumerate(context.Background(), func(got source.TrackCandidate) error {
		candidate = got
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("enumerate stop: %v", err)
	}
	candidate.MtimeNS++
	if _, err := src.Parse(context.Background(), candidate); !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("parse changed candidate: %v", err)
	}
}

func TestSourceRevisionIncludesSizeAndNanosecondMtime(t *testing.T) {
	base := sourceRevision(100, time.Unix(1_700_000_000, 1).UnixNano())
	if got := sourceRevision(100, time.Unix(1_700_000_000, 2).UnixNano()); got == base {
		t.Fatalf("same-second nanosecond change retained revision %d", got)
	}
	if got := sourceRevision(101, time.Unix(1_700_000_000, 1).UnixNano()); got == base {
		t.Fatalf("size change retained revision %d", got)
	}
}

func TestScanFindsTaggedTracks(t *testing.T) {
	root := fixtureLibrary(t)
	src, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind() != "filesystem" {
		t.Fatalf("kind: %q", src.Kind())
	}

	tracks := scanAll(t, src)
	if len(tracks) != 3 {
		t.Fatalf("want 3 tracks, got %d: %+v", len(tracks), tracks)
	}

	flac := tracks[0]
	if flac.NativeID != filepath.Join("Artist One", "Album A", "01 - Sine Song.flac") {
		t.Fatalf("native id must be the relative path: %q", flac.NativeID)
	}
	if flac.Title != "Sine Song" || flac.TrackCredits[0].Name != "Artist One" || flac.PrimaryArtist.Name != "Artist One" ||
		flac.Album != "Album A" || flac.Genre != "Rock" || flac.Year != 1994 || flac.Index != 1 {
		t.Fatalf("flac tags: %+v", flac)
	}
	if flac.Codec != "flac" || !durationClose(flac.DurationMs) || flac.SizeBytes <= 0 || flac.Version <= 0 {
		t.Fatalf("flac media: %+v", flac)
	}
	if flac.ArtHash == "" {
		t.Fatal("flac fixture has embedded art; ArtHash must be set")
	}
	if flac.TrackCredits[0].MBID != "11111111-1111-1111-1111-111111111111" || flac.RecordingMBID != "22222222-2222-2222-2222-222222222222" || flac.ReleaseMBID != "33333333-3333-3333-3333-333333333333" || flac.ReleaseGroupMBID != "44444444-4444-4444-4444-444444444444" {
		t.Fatalf("MusicBrainz tags: %+v", flac)
	}

	mp3 := tracks[1]
	if mp3.Title != "Second Song" || mp3.Index != 2 || mp3.Codec != "mp3" {
		t.Fatalf("mp3: %+v", mp3)
	}
	if mp3.ArtHash != "" {
		t.Fatalf("mp3 fixture has no art: %+v", mp3)
	}

	m4a := tracks[2]
	if m4a.TrackCredits[0].Name != "Artist Two" || m4a.Album != "Album B" || m4a.Genre != "Jazz" || m4a.Codec != "aac" {
		t.Fatalf("m4a: %+v", m4a)
	}
}

func TestOpenAndArt(t *testing.T) {
	root := fixtureLibrary(t)
	src, _ := New(root)
	tracks := scanAll(t, src)

	rc, err := src.Open(context.Background(), tracks[0].NativeID)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(rc, head); err != nil || string(head) != "fLaC" {
		t.Fatalf("open must return the audio bytes: %v %q", err, head)
	}

	candidate := candidateByID(t, src, tracks[0].NativeID)
	data, mime, err := src.Art(context.Background(), candidate, tracks[0].ArtHash)
	if err != nil || len(data) == 0 || mime == "" {
		t.Fatalf("art: %v %d bytes %q", err, len(data), mime)
	}
	if _, _, err := src.Art(context.Background(), candidateByID(t, src, tracks[1].NativeID), "missing-hash"); err == nil {
		t.Fatal("artless track must error on Art()")
	}
	if _, _, err := src.Art(context.Background(), candidate, strings.Repeat("0", 64)); err == nil || strings.Contains(err.Error(), candidate.NativeID) {
		t.Fatalf("wrong art hash must fail without exposing path: %v", err)
	}
	stale := candidate
	stale.MtimeNS++
	if _, _, err := src.Art(context.Background(), stale, tracks[0].ArtHash); !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("stale art candidate: %v", err)
	}

	// Path traversal must not escape the root.
	if _, err := src.Open(context.Background(), "../../etc/passwd"); err == nil {
		t.Fatal("open must reject escaping native ids")
	}
}

func candidateByID(t *testing.T, src *Source, nativeID string) source.TrackCandidate {
	t.Helper()
	var found source.TrackCandidate
	if err := src.Enumerate(context.Background(), func(candidate source.TrackCandidate) error {
		if candidate.NativeID == nativeID {
			found = candidate
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if found.NativeID == "" {
		t.Fatalf("candidate %q not found", nativeID)
	}
	return found
}

func TestNewRejectsMissingRoot(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing root must error")
	}
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, []byte("x"), 0o644)
	if _, err := New(f); err == nil {
		t.Fatal("non-directory root must error")
	}
}

func TestValidUUIDRejectsMalformedMusicBrainzIDs(t *testing.T) {
	valid := "12345678-1234-1234-1234-123456789abc"
	if got := validUUID("  " + valid + "  "); got != valid {
		t.Fatalf("valid UUID = %q", got)
	}
	for _, value := range []string{"", "not-a-uuid", "12345678-1234-1234-1234-123456789abz"} {
		if got := validUUID(value); got != "" {
			t.Fatalf("invalid %q accepted as %q", value, got)
		}
	}
}

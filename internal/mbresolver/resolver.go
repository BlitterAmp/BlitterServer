// Package mbresolver reconciles local albums with canonical MusicBrainz releases.
package mbresolver

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

const (
	acceptScore  = 80.0
	acceptMargin = 10.0
)

type Resolver struct {
	st        *store.Store
	client    *musicbrainz.Client
	now       func() time.Time
	batchSize int
}

func New(st *store.Store, client *musicbrainz.Client) *Resolver {
	return &Resolver{st: st, client: client, now: time.Now, batchSize: 5}
}

type artistCredit struct {
	Name       string `json:"name"`
	JoinPhrase string `json:"joinphrase"`
	Artist     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artist"`
}
type recording struct {
	ID, Title string
	Length    int            `json:"length"`
	Credits   []artistCredit `json:"artist-credit"`
}
type track struct {
	Position  int
	Recording recording
}
type medium struct {
	Position   int
	TrackCount int `json:"track-count"`
	Tracks     []track
}
type release struct {
	ID, Title, Date string
	Credits         []artistCredit `json:"artist-credit"`
	ReleaseGroup    struct {
		ID string `json:"id"`
	} `json:"release-group"`
	Media []medium `json:"media"`
}

func (r *Resolver) Run(ctx context.Context) (bool, error) {
	changed := false
	var batchErr error
	var afterDueAt int64 = -1
	var afterID string
	for {
		albums, err := r.st.DueMusicBrainzAlbumsPage(ctx, r.now(), afterDueAt, afterID, r.batchSize)
		if err != nil {
			return changed, err
		}
		if len(albums) == 0 {
			return changed, batchErr
		}
		for _, album := range albums {
			afterDueAt, afterID = album.DueAt, album.AlbumID
			if err := ctx.Err(); err != nil {
				return changed, err
			}
			c, err := r.resolve(ctx, album)
			if err != nil {
				if ctx.Err() != nil {
					return changed, ctx.Err()
				}
				batchErr = err
				continue
			}
			changed = changed || c
		}
	}
}

func (r *Resolver) resolve(ctx context.Context, album store.MusicBrainzAlbum) (bool, error) {
	var candidates []release
	if album.ReleaseID != "" {
		var direct release
		path := "/release/" + url.PathEscape(album.ReleaseID) + "?inc=release-groups%2Brecordings%2Bartist-credits&fmt=json"
		if err := r.client.GetJSON(ctx, path, &direct); err != nil {
			return false, r.recordError(ctx, album, err)
		}
		candidates = []release{direct}
	} else {
		query := fmt.Sprintf(`release:"%s" AND artist:"%s"`, escape(album.Title), escape(album.PrimaryArtist.Name))
		var body struct {
			Releases []release `json:"releases"`
		}
		if err := r.client.GetJSON(ctx, "/release?query="+url.QueryEscape(query)+"&fmt=json&limit=10", &body); err != nil {
			return false, r.recordError(ctx, album, err)
		}
		summaries := scoreCandidates(album, body.Releases)
		for _, summary := range summaries[:min(3, len(summaries))] {
			if summary.score < 70 || summary.release.ID == "" {
				continue
			}
			var detail release
			path := "/release/" + url.PathEscape(summary.release.ID) + "?inc=release-groups%2Brecordings%2Bartist-credits&fmt=json"
			if err := r.client.GetJSON(ctx, path, &detail); err != nil {
				return false, r.recordError(ctx, album, err)
			}
			candidates = append(candidates, detail)
		}
	}
	scored := scoreCandidates(album, candidates)
	persist := make([]store.MusicBrainzCandidate, 0, min(5, len(scored)))
	for _, c := range scored[:min(5, len(scored))] {
		persist = append(persist, c.persisted())
	}
	if len(scored) == 0 {
		return false, r.st.RecordMusicBrainzResult(ctx, album.AlbumID, "unmatched", store.MusicBrainzCandidate{}, nil, r.now().Add(30*24*time.Hour), "")
	}
	best := scored[0]
	if album.ReleaseID != "" && !structurallyUnique(album, best.release) {
		return false, r.st.RecordMusicBrainzResult(ctx, album.AlbumID, "ambiguous", best.persisted(), persist, r.now().Add(30*24*time.Hour), "direct release has ambiguous track positions")
	}
	margin := best.score
	if len(scored) > 1 {
		margin -= scored[1].score
	}
	if album.ReleaseID == "" && (!strongSearchMatch(album, best) || best.score < acceptScore || len(scored) > 1 && margin < acceptMargin) {
		state := "unmatched"
		if best.score >= 60 {
			state = "ambiguous"
		}
		return false, r.st.RecordMusicBrainzResult(ctx, album.AlbumID, state, best.persisted(), persist, r.now().Add(30*24*time.Hour), "")
	}
	seq, err := r.st.NextScanSeq(ctx)
	if err != nil {
		return false, err
	}
	release := canonical(best.release)
	release.Authoritative = album.ReleaseID != ""
	return r.st.ApplyMusicBrainzMatch(ctx, album, release, seq, best.persisted(), persist, r.now().Add(7*24*time.Hour))
}

func scoreCandidates(album store.MusicBrainzAlbum, candidates []release) []scoredRelease {
	scored := make([]scoredRelease, 0, len(candidates))
	for _, candidate := range candidates {
		score, evidence := scoreRelease(album, candidate)
		if album.ReleaseID != "" && candidate.ID == album.ReleaseID {
			score, evidence = 100, map[string]any{"embeddedReleaseID": true}
		}
		scored = append(scored, scoredRelease{candidate, score, evidence})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	return scored
}

func (r *Resolver) recordError(ctx context.Context, album store.MusicBrainzAlbum, cause error) error {
	_ = r.st.RecordMusicBrainzResult(ctx, album.AlbumID, "error", store.MusicBrainzCandidate{}, nil, r.now().Add(24*time.Hour), cause.Error())
	return cause
}

type scoredRelease struct {
	release  release
	score    float64
	evidence map[string]any
}

func (s scoredRelease) persisted() store.MusicBrainzCandidate {
	return store.MusicBrainzCandidate{ReleaseID: s.release.ID, ReleaseGroupID: s.release.ReleaseGroup.ID, Title: s.release.Title, ArtistCredit: creditText(s.release.Credits), Score: s.score, Evidence: s.evidence}
}

func scoreRelease(local store.MusicBrainzAlbum, candidate release) (float64, map[string]any) {
	score := 0.0
	e := map[string]any{}
	if normalize(local.Title) == normalize(candidate.Title) {
		score += 45
		e["title"] = "exact"
	}
	localArtists := map[string]bool{}
	for _, c := range local.ArtistCredits {
		localArtists[normalize(c.Name)] = true
	}
	if local.PrimaryArtist.Name != "" {
		localArtists[normalize(local.PrimaryArtist.Name)] = true
	}
	artistMatch := false
	for _, c := range candidate.Credits {
		if localArtists[normalize(c.Name)] {
			artistMatch = true
		}
		for _, lc := range local.ArtistCredits {
			if lc.MusicBrainzID != "" && lc.MusicBrainzID == c.Artist.ID {
				artistMatch = true
			}
		}
	}
	if artistMatch {
		score += 25
		e["artist"] = "matched"
	}
	if local.Year > 0 && len(candidate.Date) >= 4 && fmt.Sprint(local.Year) == candidate.Date[:4] {
		score += 10
		e["year"] = "exact"
	}
	count := 0
	durationMatches := 0
	titleMatches := 0
	positionMatches := 0
	for _, m := range candidate.Media {
		if len(m.Tracks) == 0 {
			count += m.TrackCount
		}
		for _, t := range m.Tracks {
			count++
			for _, lt := range local.Tracks {
				if lt.Disc == m.Position && lt.Index == t.Position {
					positionMatches++
					if normalize(lt.Title) == normalize(t.Recording.Title) {
						titleMatches++
					}
					if durationClose(lt.DurationMs, t.Recording.Length) {
						durationMatches++
					}
				}
			}
		}
	}
	if local.TrackCount > 0 && count == local.TrackCount {
		score += 10
		e["trackCount"] = "exact"
	}
	if local.TrackCount > 0 {
		score += 10 * float64(durationMatches) / float64(local.TrackCount)
		e["trackEvidence"] = durationMatches
		e["titleEvidence"] = titleMatches
		e["positions"] = positionMatches
	}
	return score, e
}

func strongSearchMatch(local store.MusicBrainzAlbum, candidate scoredRelease) bool {
	title, _ := candidate.evidence["title"].(string)
	artist, _ := candidate.evidence["artist"].(string)
	year, _ := candidate.evidence["year"].(string)
	positions, _ := candidate.evidence["positions"].(int)
	titles, _ := candidate.evidence["titleEvidence"].(int)
	durations, _ := candidate.evidence["trackEvidence"].(int)
	if title != "exact" || artist != "matched" || positions == 0 || titles == 0 {
		return false
	}
	if local.Year > 0 {
		return year == "exact" && (titles >= 2 || local.TrackCount == 1 && durations == 1)
	}
	return titles >= 2 && durations >= 2
}

func structurallyUnique(local store.MusicBrainzAlbum, candidate release) bool {
	for _, lt := range local.Tracks {
		if lt.Index <= 0 {
			return false
		}
		matches := 0
		for _, medium := range candidate.Media {
			for _, candidateTrack := range medium.Tracks {
				if normalizedDisc(medium.Position) == normalizedDisc(lt.Disc) && candidateTrack.Position == lt.Index {
					matches++
				}
			}
		}
		if matches != 1 {
			return false
		}
	}
	return len(local.Tracks) > 0
}

func normalizedDisc(v int) int {
	if v <= 0 {
		return 1
	}
	return v
}

func canonical(in release) store.CanonicalRelease {
	out := store.CanonicalRelease{ReleaseID: in.ID, ReleaseGroupID: in.ReleaseGroup.ID, AlbumCredits: credits(in.Credits)}
	for _, m := range in.Media {
		for _, t := range m.Tracks {
			out.Tracks = append(out.Tracks, store.CanonicalTrack{Disc: m.Position, Index: t.Position, DurationMs: t.Recording.Length, Title: t.Recording.Title, RecordingID: t.Recording.ID, Credits: credits(t.Recording.Credits)})
		}
	}
	return out
}
func credits(in []artistCredit) []source.ArtistCredit {
	out := make([]source.ArtistCredit, 0, len(in))
	for _, c := range in {
		out = append(out, source.ArtistCredit{Name: c.Name, MBID: c.Artist.ID, JoinPhrase: c.JoinPhrase})
	}
	return out
}
func creditText(in []artistCredit) string {
	var b strings.Builder
	for _, c := range in {
		b.WriteString(c.Name)
		b.WriteString(c.JoinPhrase)
	}
	return b.String()
}
func normalize(v string) string {
	v = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, v)
	return strings.Join(strings.Fields(v), " ")
}
func durationClose(a, b int) bool { return a == 0 || b == 0 || (a-b < 3000 && b-a < 3000) }
func escape(v string) string      { return strings.ReplaceAll(v, `"`, `\"`) }

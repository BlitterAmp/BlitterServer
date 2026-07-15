// Package mbresolver reconciles local albums with canonical MusicBrainz releases.
package mbresolver

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
	"golang.org/x/text/unicode/norm"
)

const (
	acceptScore  = 80.0
	acceptMargin = 10.0
)

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

func (r *Resolver) resolve(ctx context.Context, album store.MusicBrainzAlbum) (bool, error) {
	matchAlbum, fragmentSnapshots, err := r.st.MusicBrainzAlbumUnionCandidate(ctx, album)
	if err != nil {
		return false, err
	}
	var candidates []release
	scoreAlbum := matchAlbum
	if album.ReleaseID != "" {
		var direct release
		path := "/release/" + url.PathEscape(album.ReleaseID) + "?inc=release-groups%2Brecordings%2Bartist-credits&fmt=json"
		if err := r.client.GetJSON(ctx, path, &direct); err != nil {
			return false, r.recordError(ctx, album, err)
		}
		candidates = []release{direct}
	} else {
		search := func(local store.MusicBrainzAlbum, title string, withArtist bool) error {
			query := fmt.Sprintf(`release:"%s"`, escape(searchTitle(title)))
			if withArtist {
				query += fmt.Sprintf(` AND artist:"%s"`, escape(album.PrimaryArtist.Name))
			} else if album.Year > 0 {
				query += fmt.Sprintf(" AND date:%d", album.Year)
			}
			var body struct {
				Releases []release `json:"releases"`
			}
			if err := r.client.GetJSON(ctx, "/release?query="+url.QueryEscape(query)+"&fmt=json&limit=10", &body); err != nil {
				return err
			}
			summaries := scoreCandidates(local, body.Releases)
			for _, summary := range summaries[:min(3, len(summaries))] {
				if summary.score < 60 || summary.release.ID == "" {
					continue
				}
				var detail release
				path := "/release/" + url.PathEscape(summary.release.ID) + "?inc=release-groups%2Brecordings%2Bartist-credits&fmt=json"
				if err := r.client.GetJSON(ctx, path, &detail); err != nil {
					return err
				}
				candidates = append(candidates, detail)
			}
			return nil
		}
		if err := search(matchAlbum, album.Title, true); err != nil {
			return false, r.recordError(ctx, album, err)
		}
		if len(candidates) == 0 {
			if err := search(matchAlbum, album.Title, false); err != nil {
				return false, r.recordError(ctx, album, err)
			}
		}
		if len(candidates) == 0 && matchAlbum.PathTitle != "" && normalize(searchTitle(matchAlbum.PathTitle)) != normalize(searchTitle(matchAlbum.Title)) {
			scoreAlbum.Title = matchAlbum.PathTitle
			if err := search(scoreAlbum, matchAlbum.PathTitle, true); err != nil {
				return false, r.recordError(ctx, album, err)
			}
			if len(candidates) == 0 {
				if err := search(scoreAlbum, matchAlbum.PathTitle, false); err != nil {
					return false, r.recordError(ctx, album, err)
				}
			}
		}
	}
	scored := scoreCandidates(scoreAlbum, candidates)
	persist := make([]store.MusicBrainzCandidate, 0, min(5, len(scored)))
	for _, c := range scored[:min(5, len(scored))] {
		persist = append(persist, c.persisted())
	}
	if len(scored) == 0 {
		return false, r.st.RecordMusicBrainzResult(ctx, album.AlbumID, "unmatched", store.MusicBrainzCandidate{}, nil, r.now().Add(30*24*time.Hour), "")
	}
	best := scored[0]
	if album.ReleaseID != "" && !structurallyUnique(scoreAlbum, best.release) {
		return false, r.st.RecordMusicBrainzResult(ctx, album.AlbumID, "ambiguous", best.persisted(), persist, r.now().Add(30*24*time.Hour), "direct release has ambiguous track positions")
	}
	margin := best.score
	if len(scored) > 1 {
		margin -= scored[1].score
	}
	if album.ReleaseID == "" && (!strongCandidate(scoreAlbum, best) || len(scored) > 1 && margin < acceptMargin) {
		state := "unmatched"
		if best.score >= 60 {
			state = "ambiguous"
		}
		// Edition ambiguity often hides unambiguous identity: when every
		// plausible candidate agrees on the artist (and usually the release
		// group — many editions of one album), apply that shared identity so
		// artists consolidate and CAA art unlocks, while the edition itself
		// stays parked for review.
		if state == "ambiguous" {
			// Same-release-group editions tied on score: a unique structural
			// fit is the edition the local files came from; multiple fits with
			// identical aligned recordings are interchangeable for identity.
			if full, ok := editionTiebreak(scoreAlbum, scored); ok {
				seq, err := r.st.NextScanSeq(ctx)
				if err != nil {
					return false, err
				}
				release := canonical(full.release)
				return r.st.ApplyMusicBrainzMatch(ctx, album, release, seq, persistedWithFragments(full, fragmentSnapshots), persist, r.now().Add(7*24*time.Hour))
			}
			if structural, ok := structuralEditionConsensus(scoreAlbum, scored); ok && len(fragmentSnapshots) > 0 {
				seq, err := r.st.NextScanSeq(ctx)
				if err != nil {
					return false, err
				}
				release := canonical(structural.release)
				release.ReleaseID = ""
				release.ReconcileOnly = true
				return r.st.ApplyMusicBrainzConsensus(ctx, album, release, seq, persistedWithFragments(structural, fragmentSnapshots), persist, r.now().Add(30*24*time.Hour))
			}
			if partial, ok := consensusIdentity(scored); ok {
				seq, err := r.st.NextScanSeq(ctx)
				if err != nil {
					return false, err
				}
				return r.st.ApplyMusicBrainzConsensus(ctx, album, partial, seq, best.persisted(), persist, r.now().Add(30*24*time.Hour))
			}
		}
		return false, r.st.RecordMusicBrainzResult(ctx, album.AlbumID, state, best.persisted(), persist, r.now().Add(30*24*time.Hour), "")
	}
	seq, err := r.st.NextScanSeq(ctx)
	if err != nil {
		return false, err
	}
	release := canonical(best.release)
	release.Authoritative = album.ReleaseID != ""
	return r.st.ApplyMusicBrainzMatch(ctx, album, release, seq, persistedWithFragments(best, fragmentSnapshots), persist, r.now().Add(7*24*time.Hour))
}

func persistedWithFragments(candidate scoredRelease, fragments []store.MusicBrainzAlbum) store.MusicBrainzCandidate {
	persisted := candidate.persisted()
	if len(fragments) > 0 {
		persisted.Evidence["fragmentSnapshots"] = fragments
	}
	return persisted
}

// editionTiebreak resolves score-tied same-release-group editions. A single
// candidate that aligns with the local files (exact track count plus strong
// positional evidence) is the edition the files came from. Several such
// candidates are acceptable only when their aligned recordings are identical,
// making them interchangeable for identity purposes.
func editionTiebreak(album store.MusicBrainzAlbum, scored []scoredRelease) (scoredRelease, bool) {
	var fitting []scoredRelease
	for _, c := range scored {
		trackCount, _ := c.evidence["trackCount"].(string)
		if trackCount == "exact" && strongCandidate(album, c) {
			fitting = append(fitting, c)
		}
	}
	if len(fitting) > 0 {
		rgID := fitting[0].release.ReleaseGroup.ID
		if rgID == "" {
			return scoredRelease{}, false
		}
		for _, c := range fitting[1:] {
			if c.release.ReleaseGroup.ID != rgID {
				return scoredRelease{}, false
			}
		}
	}
	switch {
	case len(fitting) == 1:
		return fitting[0], true
	case len(fitting) > 1:
		reference := alignedRecordings(album, fitting[0].release)
		for _, c := range fitting[1:] {
			other := alignedRecordings(album, c.release)
			if len(other) != len(reference) {
				return scoredRelease{}, false
			}
			for position, recordingID := range reference {
				if other[position] != recordingID {
					return scoredRelease{}, false
				}
			}
		}
		return fitting[0], true
	}
	return scoredRelease{}, false
}

func structuralEditionConsensus(album store.MusicBrainzAlbum, scored []scoredRelease) (scoredRelease, bool) {
	var fitting []scoredRelease
	for _, candidate := range scored {
		trackCount, _ := candidate.evidence["trackCount"].(string)
		if trackCount == "exact" && (strongCandidate(album, candidate) || completeDurationStructure(album, candidate)) {
			fitting = append(fitting, candidate)
		}
	}
	if len(fitting) < 2 || fitting[0].release.ReleaseGroup.ID == "" {
		return scoredRelease{}, false
	}
	group := fitting[0].release.ReleaseGroup.ID
	for _, candidate := range fitting[1:] {
		if candidate.release.ReleaseGroup.ID != group {
			return scoredRelease{}, false
		}
	}
	return fitting[0], true
}

func completeDurationStructure(local store.MusicBrainzAlbum, candidate scoredRelease) bool {
	title, _ := candidate.evidence["title"].(string)
	year, _ := candidate.evidence["year"].(string)
	trackCount, _ := candidate.evidence["trackCount"].(string)
	positions, _ := candidate.evidence["positions"].(int)
	durations, _ := candidate.evidence["trackEvidence"].(int)
	return candidate.score >= 60 && title == "exact" && year == "exact" && trackCount == "exact" &&
		positions == local.TrackCount && durations*5 >= local.TrackCount*4
}

type trackPosition struct{ disc, index int }

func alignedRecordings(album store.MusicBrainzAlbum, candidate release) map[trackPosition]string {
	out := map[trackPosition]string{}
	for _, medium := range candidate.Media {
		for _, t := range medium.Tracks {
			for _, lt := range album.Tracks {
				if normalizedDisc(lt.Disc) == normalizedDisc(medium.Position) && lt.Index == t.Position {
					out[trackPosition{normalizedDisc(medium.Position), t.Position}] = t.Recording.ID
				}
			}
		}
	}
	return out
}

// consensusIdentity extracts identity every plausible (score >= 60) candidate
// agrees on: the primary artist MBID always, the release group when shared.
// Any disagreement or missing artist id yields no partial identity.
func consensusIdentity(scored []scoredRelease) (store.CanonicalRelease, bool) {
	var plausible []scoredRelease
	for _, c := range scored {
		if c.score >= 60 {
			plausible = append(plausible, c)
		}
	}
	if len(plausible) == 0 {
		return store.CanonicalRelease{}, false
	}
	artistID := ""
	for _, c := range plausible {
		if len(c.release.Credits) == 0 || c.release.Credits[0].Artist.ID == "" {
			return store.CanonicalRelease{}, false
		}
		id := c.release.Credits[0].Artist.ID
		if artistID == "" {
			artistID = id
		} else if artistID != id {
			return store.CanonicalRelease{}, false
		}
	}
	rgID := plausible[0].release.ReleaseGroup.ID
	for _, c := range plausible[1:] {
		if c.release.ReleaseGroup.ID != rgID {
			rgID = ""
			break
		}
	}
	partial := store.CanonicalRelease{ReleaseGroupID: rgID, AlbumCredits: canonical(plausible[0].release).AlbumCredits}
	return partial, true
}

var searchTitleSuffixes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\s*\[[^\]]*\]\s*$`),
	regexp.MustCompile(`(?i)\s*\((?:disc|cd)\s*\d+\)\s*$`),
	regexp.MustCompile(`(?i)\s*(?:-\s*)?(?:disc|cd)\s*\d+\s*$`),
	regexp.MustCompile(`(?i)\s+(?:disc|cd)\s*$`),
	regexp.MustCompile(`(?i)\s*\((?:\d{4}\s+)?remaster(?:ed)?(?:\s+\d{4})?\)\s*$`),
}

// searchTitle strips disc, catalog, and remaster designators that make local
// album titles miss otherwise-obvious MusicBrainz search results. Identity
// scoring still uses the untouched local title.
func searchTitle(title string) string {
	out := strings.TrimSpace(title)
	for changed := true; changed; {
		changed = false
		for _, suffix := range searchTitleSuffixes {
			next := strings.TrimSpace(suffix.ReplaceAllString(out, ""))
			if next != out {
				out, changed = next, next != ""
				if next == "" {
					return strings.TrimSpace(title)
				}
			}
		}
	}
	if out == "" {
		return strings.TrimSpace(title)
	}
	return out
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
	if normalize(searchTitle(local.Title)) == normalize(searchTitle(candidate.Title)) {
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
				if normalizedDisc(lt.Disc) == normalizedDisc(m.Position) && lt.Index == t.Position {
					positionMatches++
					if normalizedTrackTitle(lt.Title) == normalizedTrackTitle(t.Recording.Title) {
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
	trackCount, _ := candidate.evidence["trackCount"].(string)
	if title != "exact" || artist != "matched" || positions == 0 {
		return false
	}
	// Complete positional structure plus overwhelming title/duration evidence
	// outranks a bad embedded year. Files frequently carry reissue/import years.
	if trackCount == "exact" && positions == local.TrackCount && titles*5 >= local.TrackCount*4 {
		return true
	}
	if titles == 0 {
		return false
	}
	if local.Year > 0 {
		return year == "exact" && (titles >= 2 || local.TrackCount == 1 && durations == 1)
	}
	return titles >= 2 && durations >= 2
}

func strongCandidate(local store.MusicBrainzAlbum, candidate scoredRelease) bool {
	if candidate.score >= acceptScore && strongSearchMatch(local, candidate) {
		return true
	}
	title, _ := candidate.evidence["title"].(string)
	year, _ := candidate.evidence["year"].(string)
	trackCount, _ := candidate.evidence["trackCount"].(string)
	positions, _ := candidate.evidence["positions"].(int)
	titles, _ := candidate.evidence["titleEvidence"].(int)
	return candidate.score >= 60 && title == "exact" && year == "exact" && trackCount == "exact" &&
		positions == local.TrackCount && titles*5 >= local.TrackCount*4
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
	out := store.CanonicalRelease{ReleaseID: in.ID, ReleaseGroupID: in.ReleaseGroup.ID, Title: in.Title, ReleaseDate: in.Date, AlbumCredits: credits(in.Credits)}
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
	v = norm.NFD.String(v)
	v = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, v)
	return strings.Join(strings.Fields(v), " ")
}

var trackVersionSuffix = regexp.MustCompile(`(?i)\s*\((?:album version|original mix|radio edit)\)\s*$`)

func normalizedTrackTitle(v string) string {
	return normalize(trackVersionSuffix.ReplaceAllString(strings.TrimSpace(v), ""))
}
func durationClose(a, b int) bool { return a == 0 || b == 0 || (a-b < 3000 && b-a < 3000) }
func escape(v string) string      { return strings.ReplaceAll(v, `"`, `\"`) }

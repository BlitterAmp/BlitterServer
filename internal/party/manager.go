// Package party runs ephemeral listen parties: in-memory per the contract
// ("do not survive a server restart"), host-controlled transport, shared
// queue, server-owned timeline broadcast over the event bus.
package party

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

var (
	// ErrNotFound covers both unknown parties and parties the caller cannot
	// see (non-members without an invite).
	ErrNotFound = errors.New("party not found")
	ErrHostOnly = errors.New("host only")
)

// Member is a joined profile.
type Member struct {
	ProfileID string
	Name      string
}

// QueueItem is one queued track with attribution.
type QueueItem struct {
	ItemID  string
	AddedBy string
	Track   store.TrackRow
	played  bool
}

// State is the server-owned timeline.
type State struct {
	TrackID    string // "" before the first track starts / after the queue drains
	Paused     bool
	PositionMs int
	AsOf       time.Time
}

// Party is a live party snapshot.
type Party struct {
	PartyID       string
	Name          string
	HostProfileID string
	Members       []Member
	Queue         []QueueItem
	State         State
	CreatedAt     time.Time
}

type party struct {
	Party
	invited map[string]bool
	members map[string]bool
	current int // index into Queue; -1 = not started
}

// Manager owns all live parties.
type Manager struct {
	st  *store.Store
	bus *events.Bus

	mu      sync.Mutex
	parties map[string]*party
}

func NewManager(st *store.Store, bus *events.Bus) *Manager {
	return &Manager{st: st, bus: bus, parties: make(map[string]*party)}
}

func (m *Manager) Create(ctx context.Context, hostProfileID, name string) (Party, error) {
	prof, found, err := m.st.GetProfileRecord(ctx, hostProfileID)
	if err != nil {
		return Party{}, err
	}
	if !found {
		return Party{}, fmt.Errorf("profile %s: %w", hostProfileID, ErrNotFound)
	}
	p := &party{
		Party: Party{
			PartyID:       store.NewID("pty"),
			Name:          name,
			HostProfileID: hostProfileID,
			Members:       []Member{{ProfileID: hostProfileID, Name: prof.Name}},
			State:         State{Paused: true, AsOf: time.Now().UTC()},
			CreatedAt:     time.Now().UTC(),
		},
		invited: map[string]bool{},
		members: map[string]bool{hostProfileID: true},
		current: -1,
	}
	m.mu.Lock()
	m.parties[p.PartyID] = p
	m.mu.Unlock()
	return p.snapshot(), nil
}

func (p *party) snapshot() Party {
	out := p.Party
	out.Members = append([]Member(nil), p.Party.Members...)
	out.Queue = append([]QueueItem(nil), p.Party.Queue...)
	return out
}

// currentState freezes the timeline for broadcast: while playing, position
// extrapolates client-side from asOf.
func (p *party) currentState() State { return p.State }

func (m *Manager) locked(partyID string) (*party, func()) {
	m.mu.Lock()
	p, ok := m.parties[partyID]
	if !ok {
		m.mu.Unlock()
		return nil, func() {}
	}
	return p, m.mu.Unlock
}

// Get returns the party if the caller is a member or invited.
func (m *Manager) Get(_ context.Context, partyID, profileID string) (Party, error) {
	p, unlock := m.locked(partyID)
	defer unlock()
	if p == nil || (!p.members[profileID] && !p.invited[profileID]) {
		return Party{}, fmt.Errorf("party %s: %w", partyID, ErrNotFound)
	}
	return p.snapshot(), nil
}

// ListFor returns parties the profile has joined or is invited to.
func (m *Manager) ListFor(_ context.Context, profileID string) []Party {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Party
	for _, p := range m.parties {
		if p.members[profileID] || p.invited[profileID] {
			out = append(out, p.snapshot())
		}
	}
	return out
}

func (m *Manager) publishUpdated(ctx context.Context, p *party) {
	state := p.currentState()
	payload := map[string]any{
		"partyId":    p.PartyID,
		"paused":     state.Paused,
		"positionMs": state.PositionMs,
		"asOf":       state.AsOf.Format(time.RFC3339),
	}
	if state.TrackID != "" {
		payload["trackId"] = state.TrackID
	} else {
		payload["trackId"] = nil
	}
	for member := range p.members {
		if err := m.bus.Publish(ctx, "party.updated", member, payload); err != nil {
			logging.From(ctx).Warn("publish party.updated", "err", err)
		}
	}
}

func (m *Manager) Invite(ctx context.Context, partyID, byProfileID string, profileIDs []string) error {
	p, unlock := m.locked(partyID)
	if p == nil || !p.members[byProfileID] {
		unlock()
		return fmt.Errorf("party %s: %w", partyID, ErrNotFound)
	}
	var host store.ProfileRecord
	if rec, found, err := m.st.GetProfileRecord(ctx, byProfileID); err == nil && found {
		host = rec
	}
	invited := []string{}
	for _, id := range profileIDs {
		if _, found, err := m.st.GetProfileRecord(ctx, id); err != nil || !found {
			unlock()
			return fmt.Errorf("profile %s: %w", id, ErrNotFound)
		}
		if !p.members[id] {
			p.invited[id] = true
			invited = append(invited, id)
		}
	}
	name := p.Name
	unlock()
	for _, id := range invited {
		payload := map[string]any{
			"partyId":         partyID,
			"fromProfileId":   byProfileID,
			"fromProfileName": host.Name,
			"at":              time.Now().UTC().Format(time.RFC3339),
		}
		if name != "" {
			payload["partyName"] = name
		}
		if err := m.bus.Publish(ctx, "party.invited", id, payload); err != nil {
			logging.From(ctx).Warn("publish party.invited", "err", err)
		}
	}
	return nil
}

func (m *Manager) Join(ctx context.Context, partyID, profileID string) (Party, error) {
	prof, found, err := m.st.GetProfileRecord(ctx, profileID)
	if err != nil || !found {
		return Party{}, fmt.Errorf("profile %s: %w", profileID, ErrNotFound)
	}
	p, unlock := m.locked(partyID)
	if p == nil {
		unlock()
		return Party{}, fmt.Errorf("party %s: %w", partyID, ErrNotFound)
	}
	if !p.members[profileID] {
		p.members[profileID] = true
		delete(p.invited, profileID)
		p.Party.Members = append(p.Party.Members, Member{ProfileID: profileID, Name: prof.Name})
	}
	snap := p.snapshot()
	unlock()
	m.mu.Lock()
	pp := m.parties[partyID]
	m.mu.Unlock()
	if pp != nil {
		m.publishUpdated(ctx, pp)
	}
	return snap, nil
}

func (m *Manager) removeMember(p *party, profileID string) {
	delete(p.members, profileID)
	delete(p.invited, profileID)
	for i, mem := range p.Party.Members {
		if mem.ProfileID == profileID {
			p.Party.Members = append(p.Party.Members[:i], p.Party.Members[i+1:]...)
			break
		}
	}
}

func (m *Manager) Leave(ctx context.Context, partyID, profileID string) error {
	p, unlock := m.locked(partyID)
	if p == nil || !p.members[profileID] {
		unlock()
		return fmt.Errorf("party %s: %w", partyID, ErrNotFound)
	}
	if p.HostProfileID == profileID {
		delete(m.parties, partyID)
		unlock()
		return nil // host leaving ends the party
	}
	m.removeMember(p, profileID)
	unlock()
	m.mu.Lock()
	pp := m.parties[partyID]
	m.mu.Unlock()
	if pp != nil {
		m.publishUpdated(ctx, pp)
	}
	return nil
}

func (m *Manager) End(_ context.Context, partyID, profileID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.parties[partyID]
	if !ok {
		return fmt.Errorf("party %s: %w", partyID, ErrNotFound)
	}
	if p.HostProfileID != profileID {
		return fmt.Errorf("party %s: %w", partyID, ErrHostOnly)
	}
	delete(m.parties, partyID)
	return nil
}

func (m *Manager) Kick(ctx context.Context, partyID, byProfileID, targetProfileID string) error {
	p, unlock := m.locked(partyID)
	if p == nil || !p.members[byProfileID] {
		unlock()
		return fmt.Errorf("party %s: %w", partyID, ErrNotFound)
	}
	if p.HostProfileID != byProfileID {
		unlock()
		return fmt.Errorf("party %s: %w", partyID, ErrHostOnly)
	}
	m.removeMember(p, targetProfileID)
	unlock()
	m.mu.Lock()
	pp := m.parties[partyID]
	m.mu.Unlock()
	if pp != nil {
		m.publishUpdated(ctx, pp)
	}
	return nil
}

// AppendQueue adds owned tracks; any member may queue.
func (m *Manager) AppendQueue(ctx context.Context, partyID, profileID string, trackIDs []string) error {
	rows := make([]store.TrackRow, 0, len(trackIDs))
	for _, id := range trackIDs {
		tr, found, err := m.st.GetTrack(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("track %s: %w", id, ErrNotFound)
		}
		rows = append(rows, tr)
	}
	p, unlock := m.locked(partyID)
	if p == nil || !p.members[profileID] {
		unlock()
		return fmt.Errorf("party %s: %w", partyID, ErrNotFound)
	}
	for _, tr := range rows {
		p.Party.Queue = append(p.Party.Queue, QueueItem{
			ItemID: store.NewID("pqi"), AddedBy: profileID, Track: tr,
		})
	}
	unlock()
	m.mu.Lock()
	pp := m.parties[partyID]
	m.mu.Unlock()
	if pp != nil {
		m.publishUpdated(ctx, pp)
	}
	return nil
}

// Transport applies a host action and broadcasts the resulting timeline.
func (m *Manager) Transport(ctx context.Context, partyID, profileID, action string, positionMs int) (State, error) {
	p, unlock := m.locked(partyID)
	if p == nil || !p.members[profileID] {
		unlock()
		return State{}, fmt.Errorf("party %s: %w", partyID, ErrNotFound)
	}
	if p.HostProfileID != profileID {
		unlock()
		return State{}, fmt.Errorf("party %s: %w", partyID, ErrHostOnly)
	}
	now := time.Now().UTC()

	// Materialize the live position before mutating.
	if !p.State.Paused && p.State.TrackID != "" {
		p.State.PositionMs += int(now.Sub(p.State.AsOf).Milliseconds())
	}
	p.State.AsOf = now

	switch action {
	case "play":
		if p.State.TrackID == "" {
			if next := p.nextUnplayed(); next >= 0 {
				p.current = next
				p.Party.Queue[next].played = true
				p.State.TrackID = p.Party.Queue[next].Track.TrackID
				p.State.PositionMs = 0
			}
		}
		if p.State.TrackID != "" {
			p.State.Paused = false
		}
	case "pause":
		p.State.Paused = true
	case "seek":
		p.State.PositionMs = positionMs
	case "skip":
		if next := p.nextUnplayed(); next >= 0 {
			p.current = next
			p.Party.Queue[next].played = true
			p.State.TrackID = p.Party.Queue[next].Track.TrackID
			p.State.PositionMs = 0
			p.State.Paused = false
		} else {
			p.State.TrackID = ""
			p.State.PositionMs = 0
			p.State.Paused = true
		}
	default:
		unlock()
		return State{}, fmt.Errorf("action %q: %w", action, ErrNotFound)
	}
	state := p.currentState()
	unlock()
	m.mu.Lock()
	pp := m.parties[partyID]
	m.mu.Unlock()
	if pp != nil {
		m.publishUpdated(ctx, pp)
	}
	return state, nil
}

func (p *party) nextUnplayed() int {
	for i := p.current + 1; i < len(p.Party.Queue); i++ {
		if !p.Party.Queue[i].played {
			return i
		}
	}
	return -1
}

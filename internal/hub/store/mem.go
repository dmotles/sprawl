package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"gocloud.dev/blob/memblob"
)

// memStore is the in-memory Store implementation. It backs unit tests, the
// sprawl-side fakes, and local dev with zero external dependencies. Migrate is
// a no-op (its schema is just maps). It is safe for concurrent use.
type memStore struct {
	mu sync.Mutex

	user      *UserID // the singleton user, nil until EnsureUser
	tokens    map[TokenID]TokenRecord
	hosts     map[HostID]HostRecord
	projects  map[ProjectID]ProjectRecord
	active    map[ProjectID]ActiveHost
	sessions  map[SessionID]SessionRecord
	logins    map[LoginSessionID]LoginSessionRecord
	streams   map[SessionID][]Event
	blobStore BlobStore
	secrets   SecretResolver
}

// NewMemStore builds a memStore backed by memblob and a localsecrets keeper.
func NewMemStore() (Store, error) {
	keeper, err := newRandomKeeper()
	if err != nil {
		return nil, err
	}

	return &memStore{
		tokens:    make(map[TokenID]TokenRecord),
		hosts:     make(map[HostID]HostRecord),
		projects:  make(map[ProjectID]ProjectRecord),
		active:    make(map[ProjectID]ActiveHost),
		sessions:  make(map[SessionID]SessionRecord),
		logins:    make(map[LoginSessionID]LoginSessionRecord),
		streams:   make(map[SessionID][]Event),
		blobStore: &gocloudBlob{bucket: memblob.OpenBucket(nil)},
		secrets:   keeper,
	}, nil
}

func (m *memStore) Migrate(context.Context) error { return nil }

func (m *memStore) Ping(context.Context) error { return nil }

func (m *memStore) Close() error {
	return errors.Join(m.blobStore.Close(), m.secrets.Close())
}

func (m *memStore) EnsureUser(_ context.Context, u UserID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.user != nil {
		if *m.user == u {
			return nil // idempotent
		}
		return fmt.Errorf("memstore: user already set to %q; single-user invariant forbids %q", *m.user, u)
	}
	m.user = &u
	return nil
}

// requireUser enforces the users FK that pgStore gets from the schema: the
// referenced user must be the ensured singleton. Callers hold m.mu.
func (m *memStore) requireUser(u UserID) error {
	if m.user == nil || *m.user != u {
		return fmt.Errorf("memstore: user %q does not exist: %w", u, ErrNotFound)
	}
	return nil
}

func (m *memStore) CreateToken(_ context.Context, t TokenRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireUser(t.UserID); err != nil {
		return err
	}
	if _, ok := m.tokens[t.TokenID]; ok {
		return fmt.Errorf("memstore: token %q already exists", t.TokenID)
	}
	// Mirror the tokens_hash_uq unique index in pg.
	for _, existing := range m.tokens {
		if bytes.Equal(existing.Hash, t.Hash) {
			return fmt.Errorf("memstore: token hash already exists (token %q)", existing.TokenID)
		}
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	m.tokens[t.TokenID] = t
	return nil
}

func (m *memStore) ListTokens(_ context.Context, u UserID) ([]TokenRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TokenRecord
	for _, t := range m.tokens {
		if t.UserID == u {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].TokenID < out[j].TokenID
	})
	return out, nil
}

func (m *memStore) RevokeToken(_ context.Context, id TokenID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return fmt.Errorf("memstore: revoke token %q: %w", id, ErrNotFound)
	}
	if t.RevokedAt == nil {
		now := time.Now().UTC()
		t.RevokedAt = &now
		m.tokens[id] = t
	}
	return nil
}

func (m *memStore) UpsertHost(_ context.Context, h HostRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireUser(h.UserID); err != nil {
		return err
	}
	now := time.Now().UTC()
	existing, ok := m.hosts[h.HostID]
	if ok {
		h.FirstSeen = existing.FirstSeen
	} else if h.FirstSeen.IsZero() {
		h.FirstSeen = now
	}
	if h.LastSeen.IsZero() {
		h.LastSeen = now
	}
	m.hosts[h.HostID] = h
	return nil
}

func (m *memStore) RegisterInstance(_ context.Context, r InstanceRegistration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireUser(r.UserID); err != nil {
		return err
	}
	now := time.Now().UTC()
	h, ok := m.hosts[r.HostID]
	if !ok {
		h = HostRecord{HostID: r.HostID, FirstSeen: now}
	}
	h.UserID = r.UserID
	h.RepoLabel = r.RepoLabel
	h.LastRunID = r.RunID
	h.LastSeen = now
	m.hosts[r.HostID] = h
	return nil
}

func (m *memStore) ListInstances(context.Context) ([]InstanceRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	activeHosts := make(map[HostID]bool, len(m.active))
	for _, a := range m.active {
		activeHosts[a.HostID] = true
	}
	out := make([]InstanceRecord, 0, len(m.hosts))
	for _, h := range m.hosts {
		out = append(out, InstanceRecord{
			HostID:         h.HostID,
			RepoLabel:      h.RepoLabel,
			Active:         activeHosts[h.HostID],
			LastSeenUnixMs: h.LastSeen.UnixMilli(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostID < out[j].HostID })
	return out, nil
}

func (m *memStore) UpsertProject(_ context.Context, p ProjectRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireUser(p.UserID); err != nil {
		return err
	}
	existing, ok := m.projects[p.ProjectID]
	if ok {
		p.CreatedAt = existing.CreatedAt
	} else if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	m.projects[p.ProjectID] = p
	return nil
}

func (m *memStore) SetActiveHost(_ context.Context, project ProjectID, holder HostID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Require both project and host to exist, matching pgStore's active_host FKs.
	if _, ok := m.projects[project]; !ok {
		return fmt.Errorf("memstore: set active host: project %q: %w", project, ErrNotFound)
	}
	if _, ok := m.hosts[holder]; !ok {
		return fmt.Errorf("memstore: set active host: host %q: %w", holder, ErrNotFound)
	}
	m.active[project] = ActiveHost{
		ProjectID:   project,
		HostID:      holder,
		HeartbeatAt: time.Now().UTC(),
	}
	return nil
}

func (m *memStore) ReadActiveHost(_ context.Context, project ProjectID) (*ActiveHost, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.active[project]
	if !ok {
		return nil, fmt.Errorf("memstore: active host for %q: %w", project, ErrNotFound)
	}
	return &a, nil
}

func (m *memStore) CreateSession(_ context.Context, s SessionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireUser(s.UserID); err != nil {
		return err
	}
	// Mirror the sessions FKs on hosts (required) and projects (optional).
	if _, ok := m.hosts[s.HostID]; !ok {
		return fmt.Errorf("memstore: session host %q does not exist: %w", s.HostID, ErrNotFound)
	}
	if s.ProjectID != "" {
		if _, ok := m.projects[s.ProjectID]; !ok {
			return fmt.Errorf("memstore: session project %q does not exist: %w", s.ProjectID, ErrNotFound)
		}
	}
	if _, ok := m.sessions[s.SessionID]; ok {
		return fmt.Errorf("memstore: session %q already exists", s.SessionID)
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	m.sessions[s.SessionID] = s
	return nil
}

func (m *memStore) GetSession(_ context.Context, id SessionID) (*SessionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("memstore: session %q: %w", id, ErrNotFound)
	}
	return &s, nil
}

func (m *memStore) CreateLoginSession(_ context.Context, s LoginSessionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireUser(s.UserID); err != nil {
		return err
	}
	if _, ok := m.logins[s.SessionID]; ok {
		return fmt.Errorf("memstore: login session %q already exists", s.SessionID)
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	m.logins[s.SessionID] = s
	return nil
}

func (m *memStore) GetLoginSession(_ context.Context, id LoginSessionID) (*LoginSessionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.logins[id]
	if !ok {
		return nil, fmt.Errorf("memstore: login session %q: %w", id, ErrNotFound)
	}
	return &s, nil
}

func (m *memStore) DeleteLoginSession(_ context.Context, id LoginSessionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.logins[id]; !ok {
		return fmt.Errorf("memstore: delete login session %q: %w", id, ErrNotFound)
	}
	delete(m.logins, id)
	return nil
}

func (m *memStore) AppendStream(_ context.Context, sess SessionID, events []Event) (Seq, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.streams[sess]
	next := Seq(len(cur))
	for i := range events {
		e := events[i]
		next++
		e.Seq = next
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
		cur = append(cur, e)
	}
	m.streams[sess] = cur
	return next, nil
}

func (m *memStore) ReadStream(_ context.Context, sess SessionID, fromSeq, toSeq Seq) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, e := range m.streams[sess] {
		if e.Seq >= fromSeq && (toSeq == 0 || e.Seq <= toSeq) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memStore) HeadSeq(_ context.Context, sess SessionID) (Seq, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Seq(len(m.streams[sess])), nil
}

func (m *memStore) Blobs() BlobStore { return m.blobStore }

func (m *memStore) Secrets() SecretResolver { return m.secrets }

// compile-time assertion.
var _ Store = (*memStore)(nil)

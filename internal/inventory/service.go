package inventory

import "sync"

// Service serializes persistent updates while exposing concurrent snapshots.
type Service struct {
	store  *Store
	path   string
	limits Limits
	mu     sync.Mutex
}

// NewService creates an inventory service. Empty path disables persistence.
func NewService(store *Store, path string, limits Limits) *Service {
	return &Service{store: store, path: path, limits: limits}
}

// Snapshot returns a deterministic copy of current inventory.
func (s *Service) Snapshot() []Tag {
	return s.store.Snapshot()
}

// Load replaces state with persisted inventory.
func (s *Service) Load() error {
	tags, err := LoadFile(s.path, s.limits)
	if err != nil {
		return err
	}
	return s.store.Replace(tags)
}

// Upsert atomically inserts or replaces tag. Persistence completes before state changes.
func (s *Service) Upsert(tag Tag) (inserted bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tags := s.store.Snapshot()
	for i, current := range tags {
		if current.key() == tag.key() {
			tags[i] = tag
			if err := s.persist(tags); err != nil {
				return false, err
			}
			return false, s.store.Replace(tags)
		}
	}
	tags = append(tags, tag)
	if err := s.persist(tags); err != nil {
		return false, err
	}
	return true, s.store.Replace(tags)
}

// Delete atomically removes tag. Persistence completes before state changes.
func (s *Service) Delete(tag Tag) (deleted bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tags := s.store.Snapshot()
	for i, current := range tags {
		if current.key() != tag.key() {
			continue
		}
		tags = append(tags[:i:i], tags[i+1:]...)
		if err := s.persist(tags); err != nil {
			return false, err
		}
		return true, s.store.Replace(tags)
	}
	return false, nil
}

func (s *Service) persist(tags []Tag) error {
	if s.path == "" {
		return nil
	}
	return SaveFile(s.path, tags, s.limits)
}

package chat

import (
	"time"
)

// PruneOlderThan deletes messages older than the given age from both SQLite
// and the in-memory manager. It returns the number of rows deleted.
func (s *Store) PruneOlderThan(age time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-age)

	result, err := s.db.Exec(
		"DELETE FROM chat_messages WHERE timestamp < ?",
		cutoff.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, err
	}

	n, _ := result.RowsAffected()
	if n > 0 {
		s.mgr.PruneOlderThan(cutoff)
	}

	return int(n), nil
}

// startPruner starts a background goroutine that periodically prunes messages
// older than maxAge. It stops when s.done is closed.
func (s *Store) startPruner(interval, maxAge time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = s.PruneOlderThan(maxAge)
			case <-s.done:
				return
			}
		}
	}()
}

package collect

import "strconv"

// truncationEpisode keeps one delivery identity while a source remains
// continuously truncated. A complete pull clears the latch so a later
// regression re-arms without changing the stable template identity.
func (m *Manager) truncationEpisode(sourceID string, active bool) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !active {
		delete(m.truncated, sourceID)
		return ""
	}
	if id := m.truncated[sourceID]; id != "" {
		return id
	}
	m.truncSeq++
	id := "pull-truncated:" + sourceID + ":" +
		strconv.FormatInt(m.now().UTC().UnixNano(), 36) + "-" +
		strconv.FormatUint(m.truncSeq, 36)
	m.truncated[sourceID] = id
	return id
}

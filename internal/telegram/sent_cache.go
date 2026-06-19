package telegram

import (
	"container/list"
	"sync"
)

// sentCacheCapacity bounds the in-memory LRU. 10k entries × ~80 bytes each is
// ~800 KB — negligible. Anything colder than that gets looked up from the
// persisted telegram_sent_messages table.
const sentCacheCapacity = 10000

type sentKey struct {
	platform, chat, native string
}

// sentCache is a bounded LRU of (platform, chat_id, native_message_id) → mesh
// message id. Hot-path lookup to avoid a DB round-trip on every inbound
// message; on miss the bridge falls through to store.GetTelegramSentMessage.
type sentCache struct {
	mu       sync.Mutex
	capacity int
	idx      map[sentKey]*list.Element
	order    *list.List
}

type sentEntry struct {
	key sentKey
	val string
}

func newSentCache() *sentCache {
	return &sentCache{
		capacity: sentCacheCapacity,
		idx:      make(map[sentKey]*list.Element, sentCacheCapacity),
		order:    list.New(),
	}
}

func (c *sentCache) Put(platform, chat, native, meshID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := sentKey{platform, chat, native}
	if e, ok := c.idx[k]; ok {
		e.Value = sentEntry{key: k, val: meshID}
		c.order.MoveToFront(e)
		return
	}
	e := c.order.PushFront(sentEntry{key: k, val: meshID})
	c.idx[k] = e

	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.idx, oldest.Value.(sentEntry).key)
	}
}

// Get returns the mesh message id for the given native key, or "" on miss.
func (c *sentCache) Get(platform, chat, native string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := sentKey{platform, chat, native}
	e, ok := c.idx[k]
	if !ok {
		return ""
	}
	c.order.MoveToFront(e)
	return e.Value.(sentEntry).val
}

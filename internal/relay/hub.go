package relay

import "sync"

// hub wakes long-polls. Each key (an agent inbox or a request id) has a
// channel that is closed on notify and replaced, so every waiter wakes.
// Callers take the channel before checking the store, so a notify that lands
// between the check and the wait is never missed.
type hub struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}

func newHub() *hub { return &hub{ch: map[string]chan struct{}{}} }

func (h *hub) wait(key string) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.ch[key]
	if !ok {
		c = make(chan struct{})
		h.ch[key] = c
	}
	return c
}

func (h *hub) notify(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.ch[key]; ok {
		close(c)
		delete(h.ch, key)
	}
}

func inboxKey(agent string) string { return "inbox:" + agent }
func requestKey(id string) string  { return "req:" + id }

package stack

import (
	"math"
	"sync"
	"time"
)

// The write budget stops one Agent from filling the disk. Each change an Agent
// commits costs the bytes of its JSON payload, which holds every text the change
// stores. An Agent can spend WriteBurstBytes at once, and the budget refills by
// WriteRefillBytesPerMinute. Heavy use (20 Agents, about 25 calls a second each,
// with bodies of a few hundred bytes) spends at most about 450 KiB a minute per
// Agent, so it never runs out. The burst covers an Orchestrator that sets up
// thousands of Tasks at once, and it is larger than one request of
// MaxRequestBytes, so every valid change fits. A runaway loop is held to 1 MiB a
// minute after the burst, about 1.4 GiB a day. The Operator has no budget.
const (
	WriteBurstBytes           = 8 << 20
	WriteRefillBytesPerMinute = 1 << 20
)

// LowDiskBytes is the free space below which storage status and the browser
// view warn. At the refill rate, one runaway Agent needs about 17 hours to use it.
const LowDiskBytes = 1 << 30

// diskCheckInterval is how long a free-space measurement is reused.
const diskCheckInterval = 10 * time.Second

// writeBudget is an in-memory token bucket per Agent. It starts full, also after
// a restart. Mutate checks it before a change and charges it after the commit;
// Mutate runs one change at a time, so the two steps cannot interleave.
type writeBudget struct {
	mu     sync.Mutex
	now    func() time.Time // nil means time.Now
	agents map[string]budgetLevel
}
type budgetLevel struct {
	bytes float64
	at    time.Time
}

func (b *writeBudget) level(agent string) budgetLevel {
	now := time.Now()
	if b.now != nil {
		now = b.now()
	}
	l, ok := b.agents[agent]
	if !ok {
		return budgetLevel{WriteBurstBytes, now}
	}
	if elapsed := now.Sub(l.at).Seconds(); elapsed > 0 {
		l.bytes = math.Min(WriteBurstBytes, l.bytes+elapsed*WriteRefillBytesPerMinute/60)
	}
	l.at = now
	return l
}

// check returns rate_limited, with the seconds until cost fits, when the Agent's
// budget holds less than cost bytes.
func (b *writeBudget) check(agent string, cost int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.level(agent)
	if missing := float64(cost) - l.bytes; missing > 0 {
		wait := int64(math.Ceil(missing * 60 / WriteRefillBytesPerMinute))
		return &Error{Code: "rate_limited", Message: "this Agent wrote too much in a short time; retry the same command later", Retryable: true, RetryAfter: wait}
	}
	return nil
}
func (b *writeBudget) charge(agent string, cost int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.level(agent)
	l.bytes -= float64(cost)
	if b.agents == nil {
		b.agents = map[string]budgetLevel{}
	}
	b.agents[agent] = l
}

// diskSpace caches the free bytes of the file system that holds the database.
type diskSpace struct {
	mu    sync.Mutex
	free  func(path string) (uint64, error) // nil means diskFree
	at    time.Time
	bytes uint64
	err   error
}

func (d *diskSpace) get(path string) (uint64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.at.IsZero() || time.Since(d.at) >= diskCheckInterval {
		free := d.free
		if free == nil {
			free = diskFree
		}
		d.bytes, d.err = free(path)
		d.at = time.Now()
	}
	return d.bytes, d.err
}

// lowDisk reports whether free space is known and below LowDiskBytes.
func (s *Store) lowDisk() bool {
	free, err := s.disk.get(s.path)
	return err == nil && free < LowDiskBytes
}

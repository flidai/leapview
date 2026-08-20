package workload

// Metrics/accounting snapshots are built while the controller lock is held
// and cloned before observers receive them.
func (c *Controller) statsLocked() Stats {
	stats := Stats{MaximumRunning: c.config.MaximumRunning, MaximumQueued: c.config.MaximumQueued, MaximumMemoryBytes: c.config.MaximumMemoryBytes, Running: c.running, MemoryBytes: c.runningMemory, ClassOrder: append([]Class(nil), c.config.Classes...), Classes: make(map[Class]ClassStats, len(c.config.Classes)), Principals: make(map[string]ActorStats), Groups: make(map[string]ActorStats), Closed: c.closed}
	for _, class := range c.config.Classes {
		queue := c.queues[class]
		classStats := ClassStats{Policy: c.config.Policies[class], Running: c.runningClass[class], Queued: queue.queued, MemoryBytes: c.classMemory[class]}
		if borrowed := classStats.Running - classStats.Policy.ReservedRunning; borrowed > 0 {
			classStats.Borrowed = borrowed
		}
		stats.Queued += queue.queued
		stats.Classes[class] = classStats
	}
	for principal, value := range c.runningPrincipal {
		stats.Principals[principal] = ActorStats{Running: value.running, MemoryBytes: value.memoryBytes, Queued: c.queuedPrincipal[principal]}
	}
	for principal, queued := range c.queuedPrincipal {
		value := stats.Principals[principal]
		value.Queued = queued
		stats.Principals[principal] = value
	}
	for group, value := range c.runningGroup {
		stats.Groups[group] = ActorStats{Running: value.running, MemoryBytes: value.memoryBytes, Queued: c.queuedGroup[group]}
	}
	for group, queued := range c.queuedGroup {
		value := stats.Groups[group]
		value.Queued = queued
		stats.Groups[group] = value
	}
	return stats.Clone()
}

func (c *Controller) observeStats(stats Stats) {
	c.mu.RLock()
	observer := c.observer
	c.mu.RUnlock()
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	observer.ObserveWorkload(stats.Clone())
}

func (c *Controller) observeAdmission(event AdmissionEvent) {
	c.mu.RLock()
	observer := c.observer
	c.mu.RUnlock()
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	observer.ObserveAdmission(event.Clone())
}

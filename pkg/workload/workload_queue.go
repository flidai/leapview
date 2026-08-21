package workload

// classQueue implements per-principal FIFO with round-robin actor selection.
// Removal updates the cursor so cancellation cannot skip the next actor.
func (q *classQueue) enqueue(w *waiter) {
	actor := w.request.PrincipalID
	if _, ok := q.actors[actor]; !ok {
		q.order = append(q.order, actor)
	}
	q.actors[actor] = append(q.actors[actor], w)
	q.queued++
}

func (q *classQueue) peekEligible(eligible func(*waiter) bool) *waiter {
	if q.queued == 0 || len(q.order) == 0 {
		return nil
	}
	if q.cursor >= len(q.order) {
		q.cursor = 0
	}
	for offset := 0; offset < len(q.order); offset++ {
		index := (q.cursor + offset) % len(q.order)
		waiters := q.actors[q.order[index]]
		if len(waiters) > 0 && eligible(waiters[0]) {
			return waiters[0]
		}
	}
	return nil
}

func (q *classQueue) popEligible(eligible func(*waiter) bool) *waiter {
	if q.queued == 0 || len(q.order) == 0 {
		return nil
	}
	if q.cursor >= len(q.order) {
		q.cursor = 0
	}
	index := -1
	for offset := 0; offset < len(q.order); offset++ {
		candidate := (q.cursor + offset) % len(q.order)
		waiters := q.actors[q.order[candidate]]
		if len(waiters) > 0 && eligible(waiters[0]) {
			index = candidate
			break
		}
	}
	if index < 0 {
		return nil
	}
	actor := q.order[index]
	waiters := q.actors[actor]
	w := waiters[0]
	q.queued--
	if len(waiters) == 1 {
		delete(q.actors, actor)
		q.order = append(q.order[:index], q.order[index+1:]...)
		if len(q.order) == 0 || index >= len(q.order) {
			q.cursor = 0
		} else {
			q.cursor = index
		}
	} else {
		q.actors[actor] = waiters[1:]
		q.cursor = (index + 1) % len(q.order)
	}
	return w
}

func (q *classQueue) remove(target *waiter) bool {
	actor := target.request.PrincipalID
	waiters := q.actors[actor]
	for i, candidate := range waiters {
		if candidate != target {
			continue
		}
		q.queued--
		waiters = append(waiters[:i], waiters[i+1:]...)
		if len(waiters) > 0 {
			q.actors[actor] = waiters
			return true
		}
		delete(q.actors, actor)
		for index, queuedActor := range q.order {
			if queuedActor != actor {
				continue
			}
			q.order = append(q.order[:index], q.order[index+1:]...)
			if index < q.cursor {
				q.cursor--
			}
			if len(q.order) == 0 || q.cursor >= len(q.order) {
				q.cursor = 0
			}
			break
		}
		return true
	}
	return false
}

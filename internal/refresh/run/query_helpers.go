package run

// LatestRecord returns the first record from a backend-ordered target query.
// Persistence adapters share this empty-result contract while retaining their
// own SQL and transaction boundaries.
func LatestRecord(records []RunRecord) (RunRecord, bool) {
	if len(records) == 0 {
		return RunRecord{}, false
	}
	return records[0], true
}

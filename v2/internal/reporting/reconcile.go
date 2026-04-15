package reporting

import "sync"

// Pending restic-stats reconcile requests. Populated by the reporter after a
// successful heartbeat response (server says "I want fresh stats for these
// jobs"), drained by the daemon before assembling the next heartbeat.
//
// In-memory only: a daemon restart drops the pending set; the server will
// re-request on the next heartbeat. Good enough.
var (
	pendingMu       sync.Mutex
	pendingReconcile = map[string]struct{}{}
)

// RequestReconcile adds job IDs the server asked for stats on. Safe to call
// with duplicates or an empty slice.
func RequestReconcile(ids []string) {
	if len(ids) == 0 {
		return
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for _, id := range ids {
		pendingReconcile[id] = struct{}{}
	}
}

// DrainReconcile returns the current pending set and clears it. The caller
// commits to running stats for every returned ID; anything not successfully
// attached to the next heartbeat is gone until the server re-requests.
func DrainReconcile() []string {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if len(pendingReconcile) == 0 {
		return nil
	}
	out := make([]string, 0, len(pendingReconcile))
	for id := range pendingReconcile {
		out = append(out, id)
	}
	pendingReconcile = map[string]struct{}{}
	return out
}

// AttachRepoSizes mutates status.Jobs[].Result.RepoSizeBytes for jobs with a
// computed size in the map. Jobs without a result object in NodeStatus get
// a fresh one so the field has somewhere to land.
func AttachRepoSizes(status *NodeStatus, sizes map[string]int64) {
	if len(sizes) == 0 {
		return
	}
	for i := range status.Jobs {
		js := &status.Jobs[i]
		size, ok := sizes[js.ID]
		if !ok {
			continue
		}
		if js.Result == nil {
			js.Result = &JobResult{}
		}
		js.Result.RepoSizeBytes = size
	}
}

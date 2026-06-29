// SPDX-License-Identifier: Apache-2.0

// Package messages — jobs.go is the job grouping query: a job is the set of
// records sharing a job_token, folded on demand into a derived state. Nothing
// here is stored — Job/Jobs read live over the records each call. State is read,
// never projected: a terminal job_update (or a synchronous terminal response) in
// the store is the job's terminal state; otherwise the job is running.
package messages

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/solutionsunity/suctl/sdk/protocol"
)

// Job is the derived view of one job_token: the owning module/capability (read
// from the originating exchange's attribution) and the folded lifecycle state.
// QueuedAt is the enqueue time (the originating request's ReqReceived); StartedAt
// is the promotion stamp (the store's Started fact), zero while still queued.
type Job struct {
	Token      string
	Module     string
	Capability string
	State      string // queued, running, done, failed — all derived
	QueuedAt   time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Output     json.RawMessage
	Error      *protocol.ErrorDetail
	Message    string
	Messages   []string
	Progress   int
}

// Job derives the job for token from its records; ok is false if no record
// carries the token. The originating exchange supplies module/capability and the
// enqueue time; the Started fact supplies the promotion time; job_update reports
// and the synchronous response supply the terminal state. State is read, never
// projected: terminal record ⇒ done|failed, else Started ⇒ running, else queued.
func (s *Store) Job(token string) (Job, bool) {
	recs := s.ByToken(token)
	if len(recs) == 0 {
		return Job{}, false
	}
	orig := recs[0]
	for i := range recs {
		if recs[i].Request.Cmd == "invoke" {
			orig = recs[i]
			break
		}
	}
	startedAt, started := s.StartedAt(token)
	j := Job{
		Token:      token,
		Module:     orig.Module,
		Capability: orig.Capability,
		QueuedAt:   orig.ReqReceived,
		StartedAt:  startedAt,
	}
	for i := range recs {
		if recs[i].Request.Cmd != "job_update" {
			continue
		}
		j.foldUpdate(updateParams(recs[i].Request), recs[i].ReqReceived)
	}
	if j.State == "done" || j.State == "failed" {
		return j, true
	}
	// No terminal report — read the originating exchange's response: a sync
	// capability's response is terminal, an accepted async ack leaves it running.
	if orig.Done() {
		switch orig.Response.Status {
		case "error":
			j.State = "failed"
			j.FinishedAt = orig.RespReceived
			j.Error = orig.Response.Error
			return j, true
		case "ok":
			var ir protocol.InvokeResponse
			_ = json.Unmarshal(orig.Response.Result, &ir)
			if !ir.Accepted {
				j.State = "done"
				j.FinishedAt = orig.RespReceived
				if ir.Name != "" {
					j.Output = ir.Output
				} else {
					j.Output = orig.Response.Result
				}
				return j, true
			}
		}
	}
	// Not terminal — queued until the queue manager promotes it, running once the
	// Started fact is stamped.
	if started {
		j.State = "running"
	} else {
		j.State = "queued"
	}
	return j, true
}

// Jobs derives every job, optionally filtered to a single owning module short
// name (empty = all). Promoted jobs come first, newest start first; still-queued
// jobs (no Started stamp) sort after them, oldest enqueue first — the admission
// order — so the ordering is total and deterministic across both groups.
func (s *Store) Jobs(moduleFilter string) []Job {
	out := make([]Job, 0)
	for _, t := range s.jobTokens() {
		j, ok := s.Job(t)
		if !ok {
			continue
		}
		if moduleFilter != "" && j.Module != moduleFilter {
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool {
		si, sk := out[i].StartedAt, out[k].StartedAt
		if si.IsZero() != sk.IsZero() {
			return !si.IsZero() // promoted jobs sort before still-queued ones
		}
		if si.IsZero() {
			return out[i].QueuedAt.Before(out[k].QueuedAt) // both queued: oldest first
		}
		return si.After(sk) // both promoted: newest first
	})
	return out
}

// Running derives the jobs the queue manager has promoted that are not yet
// terminal — the busy set's source. Order is unspecified.
func (s *Store) Running() []Job {
	var out []Job
	for _, t := range s.jobTokens() {
		if j, ok := s.Job(t); ok && j.State == "running" {
			out = append(out, j)
		}
	}
	return out
}

// Queued derives the jobs awaiting promotion, oldest first by enqueue time — the
// FIFO admission order the queue manager scans.
func (s *Store) Queued() []Job {
	var out []Job
	for _, t := range s.jobTokens() {
		if j, ok := s.Job(t); ok && j.State == "queued" {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].QueuedAt.Before(out[k].QueuedAt) })
	return out
}

// jobTokens returns each distinct job_token present in the store, newest first
// (one job per token — grouping is grouping, not storage).
func (s *Store) jobTokens() []string {
	seen := make(map[string]bool)
	var tokens []string
	for _, r := range s.List(0) {
		t := r.JobToken()
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tokens = append(tokens, t)
	}
	return tokens
}

// foldUpdate folds one job_update report onto the job, ignoring zero fields so a
// module reports only what changed. A done/failed state stamps FinishedAt.
func (j *Job) foldUpdate(u protocol.JobUpdateParams, at time.Time) {
	if u.State != "" {
		j.State = u.State
	}
	if u.Message != "" {
		j.Message = u.Message
		j.Messages = append(j.Messages, u.Message)
	}
	if u.Progress > 0 {
		j.Progress = u.Progress
	}
	if u.Output != nil {
		j.Output = u.Output
	}
	if u.Error != nil {
		j.Error = u.Error
	}
	if j.State == "done" || j.State == "failed" {
		j.FinishedAt = at
	}
}

// updateParams decodes a job_update request's params into JobUpdateParams.
func updateParams(req protocol.Request) protocol.JobUpdateParams {
	var p protocol.JobUpdateParams
	if b, err := json.Marshal(req.Params); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

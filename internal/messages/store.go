// SPDX-License-Identifier: Apache-2.0

// Package messages is core's messages store: the recorded work. A Record is one
// exchange — a request envelope paired with its response — keyed by the request
// id. The broker is the store's sole runtime writer (open on request, complete
// on response). A job is not stored: it is the set of records sharing a
// job_token, grouped on demand (grouping is grouping, not storage). All state is
// in-memory only: core restart = full system restart, so there is nothing to
// recover.
package messages

import (
	"sync"
	"time"

	"github.com/solutionsunity/suctl/sdk/protocol"
)

// maxRecords caps the retained exchanges. Trimming is amortized: the store grows
// to twice the cap, then the newest maxRecords survive. Non-terminal (open)
// records are never evicted — only completed exchanges are trimmed.
const maxRecords = 4096

// Record is one stored exchange — a full, self-describing message: the request
// envelope and its response (filled when it returns), the receive time core
// stamped in each direction, the resolved module + capability this exchange was
// routed to, and the calling module (Caller, empty for the system/face
// originator). id (Request.ID) is the key; job_token (Request.JobToken) is the
// grouping bucket, empty for non-job exchanges.
type Record struct {
	Request      protocol.Request
	Response     protocol.Response
	ReqReceived  time.Time
	RespReceived time.Time
	Module       string
	Capability   string
	Caller       string
}

// ID returns the record's key — the request envelope id.
func (r Record) ID() string { return r.Request.ID }

// JobToken returns the record's grouping bucket, empty for non-job exchanges.
func (r Record) JobToken() string { return r.Request.JobToken }

// Done reports whether the response side has been recorded.
func (r Record) Done() bool { return !r.RespReceived.IsZero() }

// Store is the in-memory messages store: request id -> exchange record. The
// broker is the sole runtime writer. Safe for concurrent use.
//
// started holds the one fact the envelopes cannot carry: when the queue manager
// promoted a job from queued to running, keyed by job_token. It is not derivable
// from the recorded messages (it is core's own promotion decision), so it is the
// single stored fact beside the records. Everything else — queued, running, done,
// failed — is derived (see jobs.go).
type Store struct {
	mu      sync.RWMutex
	records map[string]*Record
	order   []string             // insertion order of ids, for survey and trimming
	started map[string]time.Time // job_token -> promotion (Started) stamp
}

// New returns an empty messages store. It is the boot-time builder's writer; once
// core is running the broker is the store's sole writer.
func New() *Store {
	return &Store{
		records: make(map[string]*Record),
		started: make(map[string]time.Time),
	}
}

// Start stamps the job's promotion fact — the moment the queue manager admitted
// token to run — once. Re-stamping is a no-op so a repeated advance() never
// rewrites a running job's start time. The broker is the sole caller.
func (s *Store) Start(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.started[token]; !ok {
		s.started[token] = time.Now()
	}
}

// StartedAt returns the promotion stamp for token; ok is false while the job is
// still queued (never promoted).
func (s *Store) StartedAt(token string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.started[token]
	return t, ok
}

// Open records the request side of an exchange and stamps ReqReceived. module and
// capability are the resolved routing attribution; caller is the calling module
// (empty for the system/face originator). Re-opening an existing id (a retry that
// reuses the request id) overwrites the prior open record in place.
func (s *Store) Open(req protocol.Request, module, capability, caller string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[req.ID]; !exists {
		s.order = append(s.order, req.ID)
	}
	s.records[req.ID] = &Record{
		Request:     req,
		ReqReceived: time.Now(),
		Module:      module,
		Capability:  capability,
		Caller:      caller,
	}
	s.trimLocked()
}

// Complete fills the response side of an open exchange and stamps RespReceived.
// Returns false if no record matches id (an orphan response — dropped).
func (s *Store) Complete(id string, resp protocol.Response) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return false
	}
	r.Response = resp
	r.RespReceived = time.Now()
	return true
}

// Lookup returns a copy of the record for id; ok is false if unknown.
func (s *Store) Lookup(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	if !ok {
		return Record{}, false
	}
	return *r, true
}

// List returns copies of the most recent records, newest first, up to limit
// (0 = all).
func (s *Store) List(limit int) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.order)
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]Record, 0, n)
	for i := len(s.order) - 1; i >= 0 && len(out) < n; i-- {
		if r, ok := s.records[s.order[i]]; ok {
			out = append(out, *r)
		}
	}
	return out
}

// ByToken returns copies of every record sharing job_token, oldest first. This is
// the grouping primitive a job query folds — grouping is not storage.
func (s *Store) ByToken(token string) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Record{}
	for _, id := range s.order {
		if r, ok := s.records[id]; ok && r.Request.JobToken == token {
			out = append(out, *r)
		}
	}
	return out
}

// trimLocked evicts the oldest completed exchanges once the store grows past
// twice the cap, preserving the newest maxRecords and never evicting a
// non-terminal (open) record. Caller holds s.mu.
func (s *Store) trimLocked() {
	if len(s.order) < maxRecords*2 {
		return
	}
	excess := len(s.order) - maxRecords
	keep := make([]string, 0, maxRecords)
	for _, id := range s.order {
		if r, ok := s.records[id]; ok && excess > 0 && r.Done() {
			delete(s.records, id)
			excess--
			continue
		}
		keep = append(keep, id)
	}
	s.order = keep

	// Drop Started stamps whose job has no surviving record — a trimmed terminal
	// job leaves no token behind, so its promotion fact is dead weight.
	live := make(map[string]bool, len(s.records))
	for _, r := range s.records {
		if t := r.Request.JobToken; t != "" {
			live[t] = true
		}
	}
	for token := range s.started {
		if !live[token] {
			delete(s.started, token)
		}
	}
}

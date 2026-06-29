// SPDX-License-Identifier: Apache-2.0

// Package surface implements the surface orchestrator: the face's single door
// for loading and refreshing a surface. It is storeless — it owns no truth and
// writes nothing — and it sits in front of the broker.
//
// It is the in-process initiator for face-originated work: it mints the
// originating request id and job_token, builds a complete envelope, and hands
// it to the broker. The broker originates nothing — it records and routes the
// envelope it receives, exactly as it does for one arriving over the wire. This
// consolidates the identity-minting that used to be scattered across the face
// (a job_token per call) and the broker (a defensive id, a job_token fallback)
// into one owner.
//
// Beyond minting identity it is the surface's composition authority: it fires a
// capability, unwraps the invoke envelope, and decodes the module output into
// neutral SDK responses (survey / focus / inventory). The face adapts those into
// its own render model and never speaks the wire, so the composition logic is no
// longer trapped in face (bubbletea) idioms.
package surface

import (
	"encoding/json"

	"github.com/solutionsunity/suctl/sdk/protocol"
	sdksurface "github.com/solutionsunity/suctl/sdk/surface"
	sdksystem "github.com/solutionsunity/suctl/sdk/system"
)

// Broker is the orchestrator's one downstream: the broker's in-process invoke
// surface. It takes a complete, caller-minted envelope and dispatches it under
// the originating face identity. *broker.Broker satisfies this; the interface
// keeps the orchestrator decoupled from the broker package.
type Broker interface {
	InvokeEnvelope(req *protocol.Request) (*protocol.Response, error)
}

// updatesBuffer is the depth of the inbound delivery stream. The face drains it
// continuously, so it only buffers a transient burst; it is generous enough that
// a momentarily-busy face never makes the broker drop a report in practice.
const updatesBuffer = 64

// JobUpdate is one neutral inbound delivery: the job_token it concerns and the
// module's report (state, progress, output, error). It carries no UI type — the
// face correlates token→projection and adapts params into its own render model.
type JobUpdate struct {
	Token  string
	Params protocol.JobUpdateParams
}

// Orchestrator is the storeless surface/composition authority. It holds only the
// broker and a buffered inbound stream; it caches no truth, so every load is a
// fresh read. The stream is the orchestrator's inbound (two-way) half: the
// broker pushes a surface-originated job_update through Deliver, and the face
// drains it through Updates — a neutral conduit, no state folded here.
type Orchestrator struct {
	broker  Broker
	updates chan JobUpdate
}

// New builds the orchestrator over the broker's invoke surface.
func New(b Broker) *Orchestrator {
	return &Orchestrator{broker: b, updates: make(chan JobUpdate, updatesBuffer)}
}

// Deliver is the broker's surface sink: the broker calls it for every job_update
// on a surface-originated job, after recording it. It is the orchestrator's
// inbound half — it routes the neutral report onto the stream the face drains. A
// full buffer drops the report rather than stalling the broker's wire read loop;
// the face's next fresh load reconciles truth, so liveness of core wins over a
// single dropped progress tick.
func (o *Orchestrator) Deliver(token string, params protocol.JobUpdateParams) {
	select {
	case o.updates <- JobUpdate{Token: token, Params: params}:
	default:
	}
}

// Updates is the face's inbound stream of job_update deliveries. The face drains
// it (a blocking read in a face-side command) and adapts each JobUpdate into its
// render model. Read-only to callers.
func (o *Orchestrator) Updates() <-chan JobUpdate {
	return o.updates
}

// Invoke names a capability and returns its response, minting a fresh job_token.
// As the initiator the orchestrator mints the originating id and job_token and
// builds the wire-shape envelope ({name, args}), so the face never mints
// identity and the broker originates nothing. The error contract mirrors the
// broker's: a protocol error is returned as a wrapped *protocol.ErrorDetail,
// never a transport error.
func (o *Orchestrator) Invoke(callableName string, args interface{}) (*protocol.Response, error) {
	return o.invoke(protocol.NewJobToken(), callableName, args)
}

// invoke builds and fires the complete invoke envelope under the given token.
// LoadSurvey mints the token first so it can return it to the face as the
// correlation key for an async completion; the other loaders use Invoke, which
// mints one they discard.
func (o *Orchestrator) invoke(token, callableName string, args interface{}) (*protocol.Response, error) {
	req := &protocol.Request{
		V:        protocol.Version,
		ID:       protocol.NewID(),
		TsSent:   protocol.Timestamp(),
		Cmd:      "invoke",
		JobToken: token,
		Params:   map[string]interface{}{"name": callableName, "args": args},
	}
	return o.broker.InvokeEnvelope(req)
}

// unwrapInvokeOutput extracts the output field from an invoke envelope. Modules
// wrap a synchronous response as {"name":"...","output":{...}}; an async cap
// returns {"name":"...","accepted":true} with no output yet, in which case the
// raw result is returned unchanged (it decodes to no subjects/fields).
func unwrapInvokeOutput(result json.RawMessage) json.RawMessage {
	var ir protocol.InvokeResponse
	if err := json.Unmarshal(result, &ir); err != nil || ir.Output == nil {
		return result
	}
	return ir.Output
}

// DecodeSurveyOutput decodes a bare survey output payload — the {total,
// status_summary, subjects, actions} object a module returns — into the neutral
// survey response plus the survey-level (bulk) actions the module enabled. It is
// the single decode shared by both result carriers: the inline output of a sync
// invoke (peeled from InvokeResponse.output) and the output pushed on a terminal
// async job_update (JobUpdateParams.output). Sync is the degenerate case — one
// payload, one decode. An empty or subject-less payload (e.g. an async accept
// ack) yields a zero-value response and nil actions.
func DecodeSurveyOutput(output json.RawMessage) (sdksurface.SurveyResponse, []sdksurface.Action) {
	var sr sdksurface.SurveyResponse
	var wrapper struct {
		Total         int                  `json:"total"`
		StatusSummary string               `json:"status_summary"`
		Subjects      []sdksurface.Subject `json:"subjects"`
		Actions       []sdksurface.Action  `json:"actions"`
	}
	if err := json.Unmarshal(output, &wrapper); err != nil || wrapper.Subjects == nil {
		return sr, nil
	}
	sr.Total = wrapper.Total
	sr.StatusSummary = wrapper.StatusSummary
	sr.Subjects = wrapper.Subjects
	return sr, wrapper.Actions
}

// DecodeFocusOutput decodes a bare focus output payload into the neutral focus
// response. Like DecodeSurveyOutput it serves both carriers (inline and pushed);
// a malformed or empty payload decodes to a zero-value response.
func DecodeFocusOutput(output json.RawMessage) sdksurface.FocusResponse {
	var fr sdksurface.FocusResponse
	json.Unmarshal(output, &fr) //nolint:errcheck
	return fr
}

// DecodeCellOutput decodes a whole-column cell-fill payload — the row-keyed
// {rows:{rowID:{columns:{…}}}} map a column's `from` capability returns for every
// row at once — into the neutral rowID→column→cell map. Like the survey/focus
// decoders it serves both carriers (the inline output of a sync invoke and the
// output pushed on a terminal async job_update); a malformed, empty, or row-less
// payload decodes to a nil map. The outer keys are row ids and the inner keys are
// column ids: the face applies each row's map to the matching columns of that
// row, so one call fills every column sharing that `from` across all rows.
func DecodeCellOutput(output json.RawMessage) map[string]map[string]sdksurface.Column {
	var wrapper struct {
		Rows map[string]struct {
			Columns map[string]sdksurface.Column `json:"columns"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(output, &wrapper); err != nil || wrapper.Rows == nil {
		return nil
	}
	rows := make(map[string]map[string]sdksurface.Column, len(wrapper.Rows))
	for rowID, r := range wrapper.Rows {
		rows[rowID] = r.Columns
	}
	return rows
}

// SurveyLoad is the outcome of firing a survey capability. Token is the minted
// job_token — the correlation key the face keeps to match a completion pushed
// later. Accepted is true when the module took the work async: Survey/Actions
// are empty now and the real result arrives via a job_update keyed by Token.
// When Accepted is false the module answered inline and Survey/Actions are
// populated. Sync is the degenerate case — Accepted=false, the first update is
// also the last and rides back on the response.
type SurveyLoad struct {
	Token    string
	Accepted bool
	Survey   sdksurface.SurveyResponse
	Actions  []sdksurface.Action
}

// LoadSurvey fires a survey capability and returns a SurveyLoad: the minted
// token, whether the module accepted it async, and (when inline) the decoded
// survey plus the survey-level (bulk) actions the module enabled. Modules return
// all rows tagged with applicable facets; the face filters locally — no facets
// are sent to the module. A protocol error is returned as a wrapped
// *protocol.ErrorDetail (Token still set so the caller can clear any pending
// state).
func (o *Orchestrator) LoadSurvey(capName string, args map[string]interface{}) (SurveyLoad, error) {
	if args == nil {
		args = map[string]interface{}{}
	}
	token := protocol.NewJobToken()
	resp, err := o.invoke(token, capName, args)
	if err != nil {
		return SurveyLoad{Token: token}, err
	}
	var ir protocol.InvokeResponse
	if json.Unmarshal(resp.Result, &ir) == nil && ir.Accepted {
		return SurveyLoad{Token: token, Accepted: true}, nil
	}
	sr, actions := DecodeSurveyOutput(unwrapInvokeOutput(resp.Result))
	return SurveyLoad{Token: token, Survey: sr, Actions: actions}, nil
}

// LoadFocus fires a focus capability for a subject and decodes the focus body.
// A protocol error is returned wrapped; a malformed/empty output decodes to a
// zero-value FocusResponse with no error, mirroring LoadSurvey.
func (o *Orchestrator) LoadFocus(capName, subjectID string) (sdksurface.FocusResponse, error) {
	resp, err := o.Invoke(capName, map[string]interface{}{"subject": subjectID})
	if err != nil {
		return sdksurface.FocusResponse{}, err
	}
	return DecodeFocusOutput(unwrapInvokeOutput(resp.Result)), nil
}

// CellLoad is the outcome of firing a column's `from` capability for a whole
// column (every row at once). Token is the minted job_token — the correlation key
// the face keeps to match a completion pushed later (paired face-side with the
// `from` it fills). Accepted is true when the module took the work async: Rows is
// empty now and the real row-keyed map arrives via a job_update keyed by Token.
// When Accepted is false the module answered inline and Rows is populated. Sync is
// the degenerate case — Accepted=false, the first update is also the last and
// rides back on the response.
type CellLoad struct {
	Token    string
	Accepted bool
	Rows     map[string]map[string]sdksurface.Column
}

// FillCells fires a column's `from` capability for the whole column and returns a
// CellLoad: the minted token, whether the module accepted it async, and (when
// inline) the decoded row-keyed map. args carries the survey-level context (e.g.
// {"scope": parentRow} for a drill child), opaque to core. One call fills every
// column declaring this `from` across all rows: the returned map is keyed by row
// id then column id, and the face applies each row's columns to the matching row.
// The token is minted first (like LoadSurvey) so the face can correlate an async
// completion back to the columns this `from` fills.
func (o *Orchestrator) FillCells(capName string, args map[string]interface{}) (CellLoad, error) {
	if args == nil {
		args = map[string]interface{}{}
	}
	token := protocol.NewJobToken()
	resp, err := o.invoke(token, capName, args)
	if err != nil {
		return CellLoad{Token: token}, err
	}
	var ir protocol.InvokeResponse
	if json.Unmarshal(resp.Result, &ir) == nil && ir.Accepted {
		return CellLoad{Token: token, Accepted: true}, nil
	}
	return CellLoad{Token: token, Rows: DecodeCellOutput(unwrapInvokeOutput(resp.Result))}, nil
}

// InvokeAction fires an action capability and discards the output, returning
// only success/failure. A CONFIRMATION_REQUIRED cascade is preserved in the
// returned error (recover it via protocol.AsCascade), keeping protocol.Response
// out of the face entirely.
func (o *Orchestrator) InvokeAction(capName string, args map[string]interface{}) error {
	if args == nil {
		args = map[string]interface{}{}
	}
	_, err := o.Invoke(capName, args)
	return err
}

// Inventory fires system.module.inventory and decodes the typed module index.
// This is the single read point for the inventory shared by every face.
func (o *Orchestrator) Inventory() (sdksystem.InventoryResponse, error) {
	var inv sdksystem.InventoryResponse
	resp, err := o.Invoke("system.module.inventory", map[string]interface{}{})
	if err != nil {
		return inv, err
	}
	if err := json.Unmarshal(unwrapInvokeOutput(resp.Result), &inv); err != nil {
		return inv, err
	}
	return inv, nil
}

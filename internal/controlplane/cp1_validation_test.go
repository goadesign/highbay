package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genmcpadapter "github.com/goadesign/highbay/gen/mcp_highbay"
)

// TestValidateAmbiguityAnswersReportsEveryProblemOnce verifies that
// confirm_intent's answer check accepts exact option text and otherwise
// returns one error naming every missing, invalid and unknown answer, with
// each question's options quoted. The revise_change pointer ends the error
// only when an answer is missing or is not one of its question's options. A
// question that names the option its draft implements accepts only that
// answer; a question that names none accepts each of its options.
func TestValidateAmbiguityAnswersReportsEveryProblemOnce(t *testing.T) {
	drafted := "Listing seller"
	questions := []*genhighbay.AmbiguityQuestion{
		{ID: "chg-ambiguity-01", Question: "Who signs in?", Options: []string{"Bearer token auth", "Demo persona header"}},
		{ID: "chg-ambiguity-02", Question: "Who accepts offers?", Options: []string{"Listing seller", "Any seller"}, DraftOption: &drafted},
		{ID: "chg-ambiguity-03", Question: "Keep history?", Options: []string{"Yes", "No"}},
	}
	const pointer = "; each answer must be exactly one of its question's options; for a decision that no option expresses, ask your coding agent to revise the change"
	const draftPointer = "; ask your coding agent to revise the change for each such answer, so the design, Story, examples and preserved behaviors implement it, then confirm the revised change"
	complete := map[string]string{
		"chg-ambiguity-01": "Demo persona header",
		"chg-ambiguity-02": "Listing seller",
		"chg-ambiguity-03": "No",
	}
	cases := []struct {
		name    string
		answers map[string]string
		want    string
	}{
		{
			name:    "exact options are accepted",
			answers: complete,
		},
		{
			name: "every problem is reported in question order, then unknown IDs sorted",
			answers: map[string]string{
				"chg-ambiguity-01": "Demo persona selector at the edge",
				"chg-ambiguity-03": "  ",
				"chg-ambiguity-99": "Yes",
				"chg-ambiguity-42": "No",
			},
			want: `answer "Demo persona selector at the edge" for ambiguity "chg-ambiguity-01" is not one of its options: ["Bearer token auth" "Demo persona header"]; ` +
				`missing confirmation answer for ambiguity "chg-ambiguity-02" (options: ["Listing seller" "Any seller"]); ` +
				`missing confirmation answer for ambiguity "chg-ambiguity-03" (options: ["Yes" "No"]); ` +
				`unexpected confirmation answer for unknown ambiguity "chg-ambiguity-42"; ` +
				`unexpected confirmation answer for unknown ambiguity "chg-ambiguity-99"` + pointer,
		},
		{
			name: "an option that differs only in spacing is not an option",
			answers: map[string]string{
				"chg-ambiguity-01": "Bearer token auth ",
				"chg-ambiguity-02": "Listing seller",
				"chg-ambiguity-03": "Yes",
			},
			want: `answer "Bearer token auth " for ambiguity "chg-ambiguity-01" is not one of its options: ["Bearer token auth" "Demo persona header"]` + pointer,
		},
		{
			name: "an option the draft does not implement is refused until the change is revised",
			answers: map[string]string{
				"chg-ambiguity-01": "Bearer token auth",
				"chg-ambiguity-02": "Any seller",
				"chg-ambiguity-03": "Yes",
			},
			want: `answer "Any seller" for ambiguity "chg-ambiguity-02" is not what the draft implements ("Listing seller")` + draftPointer,
		},
		{
			name: "an undrafted option is reported after unresolved answers, with both pointers",
			answers: map[string]string{
				"chg-ambiguity-01": "Bearer token auth",
				"chg-ambiguity-02": "Any seller",
			},
			want: `missing confirmation answer for ambiguity "chg-ambiguity-03" (options: ["Yes" "No"]); ` +
				`answer "Any seller" for ambiguity "chg-ambiguity-02" is not what the draft implements ("Listing seller")` + pointer + draftPointer,
		},
		{
			name: "only an unknown question ID gets no revise_change pointer",
			answers: map[string]string{
				"chg-ambiguity-01": "Demo persona header",
				"chg-ambiguity-02": "Listing seller",
				"chg-ambiguity-03": "No",
				"chg-ambiguity-04": "Yes",
			},
			want: `unexpected confirmation answer for unknown ambiguity "chg-ambiguity-04"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAmbiguityAnswers(questions, tc.answers)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
		})
	}
}

// A proposal's draft_option must be one of the question's options exactly;
// the saved question keeps it, and keeps none when the proposal names none.
func TestValidateAmbiguitiesKeepsTheDraftOption(t *testing.T) {
	second, spaced, missing := "Any seller", "Any seller ", "Nobody"
	options := []string{"Listing seller", "Any seller"}
	cases := []struct {
		// name describes the proposal's draft_option.
		name string
		// draft is the proposal's draft_option, nil when it names none.
		draft *string
		// want is the error, empty when the proposal is accepted.
		want string
	}{
		{name: "none named"},
		{name: "one of the options", draft: &second},
		{name: "an option with different spacing", draft: &spaced, want: `ambiguity "chg-ambiguity-01" (ambiguities[0]) has draft_option "Any seller ", which is not one of its options: ["Listing seller" "Any seller"]`},
		{name: "not an option", draft: &missing, want: `ambiguity "chg-ambiguity-01" (ambiguities[0]) has draft_option "Nobody", which is not one of its options: ["Listing seller" "Any seller"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateAmbiguities("chg", []*genhighbay.CP1AnalysisAmbiguity{{Question: "Who accepts offers?", Options: options, DraftOption: tc.draft}})
			if tc.want != "" {
				require.Error(t, err)
				assert.Equal(t, tc.want, err.Error())
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.draft, got[0].DraftOption)
		})
	}
}

// A card whose questions name no draft option keeps the request_revision it
// had before questions could name one, and saves no such field: saved builds
// and approvals refer to that revision. Naming one changes the revision.
func TestRequestRevisionIgnoresAnAbsentDraftOption(t *testing.T) {
	by := "user"
	card := &genhighbay.ChangeCardResult{
		ChangeID: "orders", Status: StatusConfirmed, Prompt: "p", Summary: "s",
		Ambiguities: []*genhighbay.AmbiguityQuestion{{ID: "orders-ambiguity-01", Question: "q?", Options: []string{"a", "b"}}},
		Answers:     map[string]string{"orders-ambiguity-01": "a"},
		Examples:    []*genhighbay.ExampleItem{{ID: "check_01", Scenario: "sc", InputSummary: "in", ExpectedOutcome: "out", VerificationKind: "deterministic"}},
		ConfirmedBy: &by,
	}
	rev, err := computeCanonicalRevision(card, "owner", "package design")
	require.NoError(t, err)
	assert.Equal(t, "801a1d07d2cdcab10579990cea2e983a8c7e7179eb48039dfdb700222b087bf7", rev)
	saved, err := json.Marshal(card.Ambiguities)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"ID":"orders-ambiguity-01","Question":"q?","Options":["a","b"]}]`, string(saved))

	drafted := "a"
	card.Ambiguities[0].DraftOption = &drafted
	withDraft, err := computeCanonicalRevision(card, "owner", "package design")
	require.NoError(t, err)
	assert.NotEqual(t, rev, withDraft)
}

// TestConfirmIntentRefusesAnAnswerTheDraftDoesNotImplement follows a saved
// card whose question names its draft option: the read returns it, an answer
// with another option is refused and leaves the card unconfirmed, and the
// draft's option confirms the card in place.
func TestConfirmIntentRefusesAnAnswerTheDraftDoesNotImplement(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	drafted := "original_payment"
	proposed := saveProposedCardFixture(t, cp, agreedOwner, "chg", "Refunds up to $50 are instant.", refundAnalysis(
		"Refunds up to $50 are instant.",
		&genhighbay.CP1AnalysisAmbiguity{Question: "Where does an instant refund go?", Options: []string{"original_payment", "store_credit"}, DraftOption: &drafted},
	))
	read, err := cp.GetChange(agreedUser, &genhighbay.GetChangePayload{ChangeID: "chg"})
	require.NoError(t, err)
	require.Len(t, read.Ambiguities, 1)
	require.NotNil(t, read.Ambiguities[0].DraftOption)
	assert.Equal(t, drafted, *read.Ambiguities[0].DraftOption)

	confirm := func(answer string) (*genhighbay.ChangeCardResult, error) {
		return cp.ConfirmIntent(agreedUser, &genhighbay.ConfirmIntentPayload{
			ChangeID: "chg", RequestRevision: proposed.RequestRevision,
			Answers: map[string]string{read.Ambiguities[0].ID: answer}, Examples: proposed.Examples,
		})
	}
	_, err = confirm("store_credit")
	assertServiceError(t, err, "unprocessable")
	assert.ErrorContains(t, err, `answer "store_credit" for ambiguity "chg-ambiguity-01" is not what the draft implements ("original_payment")`)
	assert.ErrorContains(t, err, "ask your coding agent to revise the change")
	unchanged, err := cp.GetChange(agreedUser, &genhighbay.GetChangePayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.Equal(t, StatusNeedsConfirmation, unchanged.Status)
	assert.Equal(t, proposed.RequestRevision, unchanged.RequestRevision)

	confirmed, err := confirm("original_payment")
	require.NoError(t, err)
	assert.Equal(t, StatusConfirmed, confirmed.Status)
	require.NotNil(t, confirmed.Ambiguities[0].DraftOption)
	assert.Equal(t, drafted, *confirmed.Ambiguities[0].DraftOption)
}

// TestChangeCardHTTPErrorsAndMCPToolPreservation verifies that established
// caller, state, and unconfigured-capability refusals across the Change Card
// HTTP routes return their declared non-fault HTTP status codes (403, 404, 409,
// 422, 501), and that MCP tools/call continues to return
// non-fault tool error results with the same domain error message.
func TestChangeCardHTTPErrorsAndMCPToolPreservation(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	aliceCtx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser})
	ambiguous := &genhighbay.CP1Analysis{
		Summary:     "Refunds up to $50 are instant.",
		Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{{Question: "Who approves?", Options: []string{"instant", "manager"}}},
		Examples: []*genhighbay.CP1AnalysisExample{{
			Scenario: "Refund at limit", InputSummary: "$50", ExpectedOutcome: "Instant", VerificationKind: "deterministic",
		}},
		PreservedBehaviors: []string{},
	}
	auth := workflowAuthenticator(map[string]Principal{
		"alice-token": {ID: "alice@example.com"},
		"bob-token":   {ID: "bob@example.com"},
	})
	// Both preview refusals use one server so its evaluator reuses its own
	// compiler workspace while the handler preserves each distinct error.
	cp := newControlPlane(t, Options{})
	pending, err := cp.ProposeChange(aliceCtx, &genhighbay.ProposeChangePayload{ChangeID: "chg", Prompt: "Refunds.", Analysis: ambiguous})
	require.NoError(t, err)
	confirmed, err := cp.ConfirmIntent(aliceCtx, &genhighbay.ConfirmIntentPayload{
		ChangeID: "chg-ok", RequestRevision: func() string {
			c, err := cp.ProposeChange(aliceCtx, &genhighbay.ProposeChangePayload{ChangeID: "chg-ok", Prompt: "Refunds.", Analysis: ambiguous})
			require.NoError(t, err)
			return c.RequestRevision
		}(),
		Answers:  map[string]string{"chg-ok-ambiguity-01": "instant"},
		Examples: pending.Examples,
	})
	require.NoError(t, err)
	handler := NewHTTPHandler(cp, auth)
	fixedHostHandler := NewHTTPHandler(newControlPlane(t, Options{}), auth)
	// proposalAnalysis and revisionAnalysis are the wire forms of complete
	// analyses, so these requests reach the service's own checks.
	proposalAnalysis := map[string]any{
		"summary":     "Refunds up to $50 are instant.",
		"ambiguities": []any{},
		"examples": []map[string]any{{
			"scenario": "Refund at limit", "input_summary": "$50", "expected_outcome": "Instant", "verification_kind": "deterministic",
		}},
		"preserved_behaviors": []string{},
	}
	revisionAnalysis := func(priorID string) map[string]any {
		return map[string]any{
			"summary":     "Refunds up to $60 are instant.",
			"ambiguities": []any{},
			"examples": []map[string]any{{
				"prior_id": priorID, "scenario": "Refund at limit", "input_summary": "$60", "expected_outcome": "Instant", "verification_kind": "deterministic",
			}},
			"removed_example_ids": []string{},
			"preserved_behaviors": []string{},
		}
	}

	for _, tc := range []struct {
		name       string
		h          http.Handler
		method     string
		path       string
		token      string
		body       map[string]any
		wantStatus int
		wantFault  bool
		wantMsg    string
	}{
		{"configure_model provider without held settings on fixed host", fixedHostHandler, http.MethodPost, "/cp1/model", "alice-token", map[string]any{"provider": "anthropic", "model_name": "claude-sonnet-4-6"}, http.StatusUnprocessableEntity, false, "provider anthropic needs ANTHROPIC_API_KEY: export it"},
		{"configure_model provider without held settings on configured host", handler, http.MethodPost, "/cp1/model", "alice-token", map[string]any{"provider": "openai", "model_name": "gpt-5.4-mini"}, http.StatusUnprocessableEntity, false, "provider openai needs OPENAI_API_KEY: export it"},
		{"propose duplicate change_id", handler, http.MethodPost, "/changes/propose", "alice-token", map[string]any{"change_id": "chg", "prompt": "p", "analysis": proposalAnalysis}, http.StatusConflict, false, `change_id "chg" already exists`},
		{"propose blank prompt", handler, http.MethodPost, "/changes/propose", "alice-token", map[string]any{"change_id": "blank", "prompt": "  ", "analysis": proposalAnalysis}, http.StatusUnprocessableEntity, false, "change_id and prompt are required"},
		{"revise unknown change_id", handler, http.MethodPost, "/changes/revise", "alice-token", map[string]any{"change_id": "missing", "request_revision": strings.Repeat("0", 64), "prompt": "p", "revision_note": "n", "analysis": revisionAnalysis("check_01")}, http.StatusNotFound, false, `unknown change_id "missing"`},
		{"revise another owner's card", handler, http.MethodPost, "/changes/revise", "bob-token", map[string]any{"change_id": "chg", "request_revision": pending.RequestRevision, "prompt": "p", "revision_note": "n", "analysis": revisionAnalysis("check_01")}, http.StatusForbidden, false, `principal "bob@example.com" is not the owner of change_id "chg"`},
		{"revise with an unknown prior example", handler, http.MethodPost, "/changes/revise", "alice-token", map[string]any{"change_id": "chg", "request_revision": pending.RequestRevision, "prompt": "p", "revision_note": "n", "analysis": revisionAnalysis("check_09")}, http.StatusUnprocessableEntity, false, `invalid analysis: examples[0].prior_id references unknown prior example ID "check_09"`},
		{"get_change unknown", handler, http.MethodGet, "/changes/missing", "alice-token", nil, http.StatusNotFound, false, `unknown change_id "missing"`},
		{"get_change wrong owner", handler, http.MethodGet, "/changes/chg", "bob-token", nil, http.StatusForbidden, false, `principal "bob@example.com" is not the owner of change_id "chg"`},
		{"revise stale revision", handler, http.MethodPost, "/changes/revise", "alice-token", map[string]any{"change_id": "chg", "request_revision": strings.Repeat("0", 64), "prompt": "p", "revision_note": "n", "analysis": revisionAnalysis("check_01")}, http.StatusConflict, false, "request_revision mismatch"},
		{"confirm invalid answer", handler, http.MethodPost, "/changes/confirm", "alice-token", map[string]any{
			"change_id":        "chg",
			"request_revision": pending.RequestRevision,
			"answers":          map[string]string{"chg-ambiguity-01": "wrong"},
			"examples": []map[string]any{{
				"id":                pending.Examples[0].ID,
				"scenario":          pending.Examples[0].Scenario,
				"input_summary":     pending.Examples[0].InputSummary,
				"expected_outcome":  pending.Examples[0].ExpectedOutcome,
				"verification_kind": pending.Examples[0].VerificationKind,
			}},
		}, http.StatusUnprocessableEntity, false, "is not one of its options"},
		{"preview unconfirmed card", handler, http.MethodPost, "/changes/preview", "alice-token", map[string]any{"change_id": "chg", "request_revision": pending.RequestRevision}, http.StatusConflict, false, `change_id "chg" must be confirmed before architecture preview (status "needs_confirmation")`},
		{"preview without saved architecture", handler, http.MethodPost, "/changes/preview", "alice-token", map[string]any{"change_id": "chg-ok", "request_revision": confirmed.RequestRevision}, http.StatusUnprocessableEntity, false, "no architecture to show"},
		{"verify_change without config", handler, http.MethodPost, "/changes/chg-ok/verifications", "alice-token", map[string]any{"request_revision": confirmed.RequestRevision, "candidate_commit": strings.Repeat("a", 40)}, http.StatusNotImplemented, false, "verification is not configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			if tc.body != nil {
				var err error
				raw, err = json.Marshal(tc.body)
				require.NoError(t, err)
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(string(raw)))
			workflowAuthorization(req, tc.token)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			tc.h.ServeHTTP(rec, req)
			assert.Equal(t, tc.wantStatus, rec.Code)
			var errBody struct {
				Message string `json:"message"`
				Fault   bool   `json:"fault"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errBody), rec.Body.String())
			assert.Equal(t, tc.wantFault, errBody.Fault)
			assert.Contains(t, errBody.Message, tc.wantMsg)
		})
	}

	adapter := genmcpadapter.NewMCPAdapter(genhighbay.NewEndpoints(cp), nil)
	argsJSON, err := json.Marshal(map[string]any{
		"change_id": "chg", "request_revision": pending.RequestRevision,
	})
	require.NoError(t, err)
	mcpRes, err := adapter.ToolsCall(aliceCtx, &genmcpadapter.ToolsCallPayload{Name: "preview_architecture", Arguments: argsJSON})
	require.NoError(t, err)
	complete, ok := mcpRes.Outcome.AsComplete()
	require.True(t, ok)
	require.NotNil(t, complete.IsError)
	assert.True(t, *complete.IsError)
	require.NotEmpty(t, complete.Content)
	require.NotNil(t, complete.Content[0].Text)
	assert.Contains(t, *complete.Content[0].Text, `change_id "chg" must be confirmed before architecture preview (status "needs_confirmation")`)
}

// TestAnalysisTextsMustHoldMoreThanWhitespace verifies the design's non-blank
// rule on every required analysis text (summary, question, option, scenario,
// input_summary, expected_outcome and preserved behavior) of propose_change
// and revise_change. A text made only of whitespace, as unicode.IsSpace
// defines it (ASCII, U+00A0, U+2003, U+3000, U+0085 or a mix), is refused and
// nothing is saved: every case through the Go method, representative cases
// through the MCP tool and the HTTP route. A text with any other character,
// including one made only of U+200B or U+FEFF, is saved byte for byte as sent,
// surrounding whitespace included.
func TestAnalysisTextsMustHoldMoreThanWhitespace(t *testing.T) {
	alice := WithPrincipal(context.Background(), Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser})
	cp := newControlPlane(t, Options{})
	adapter := genmcpadapter.NewMCPAdapter(genhighbay.NewEndpoints(cp), nil)
	srv := httptest.NewServer(NewHTTPHandler(cp, workflowAuthenticator(map[string]Principal{"alice-token": {ID: "alice@example.com"}})))
	t.Cleanup(srv.Close)

	// The test source names each character by code point so it stays ASCII.
	noBreakSpace := string(rune(0x00a0))
	emSpace := string(rune(0x2003))
	ideographicSpace := string(rune(0x3000))
	nextLine := string(rune(0x0085))
	byteOrderMark := string(rune(0xfeff))
	zeroWidthSpace := string(rune(0x200b))
	check01 := "check_01"

	// proposal and revision build the analyses with every required text
	// passed through text, so one case can pad or replace all of them.
	proposal := func(text func(string) string) *genhighbay.CP1Analysis {
		return &genhighbay.CP1Analysis{
			Summary: text("Refunds up to $50 are instant."),
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{{
				Question: text("Is $50.00 itself instant?"),
				Options:  []string{"instant", text("manager")},
			}},
			Examples: []*genhighbay.CP1AnalysisExample{{
				Scenario: text("Refund at the limit"), InputSummary: text("Order total $50.00"),
				ExpectedOutcome: text("Instant store credit"), VerificationKind: "deterministic",
			}},
			PreservedBehaviors: []string{text("Cross-tenant order lookups return 404 Not Found.")},
		}
	}
	revision := func(text func(string) string) *genhighbay.CP1RevisionAnalysis {
		return &genhighbay.CP1RevisionAnalysis{
			Summary: text("Refunds up to $50 are instant."),
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{{
				Question: text("Is $50.00 itself instant?"),
				Options:  []string{"instant", text("manager")},
			}},
			Examples: []*genhighbay.CP1RevisionAnalysisExample{{
				PriorID: &check01, Scenario: text("Refund at the limit"), InputSummary: text("Order total $50.00"),
				ExpectedOutcome: text("Instant store credit"), VerificationKind: "deterministic",
			}},
			RemovedExampleIds:  []string{},
			PreservedBehaviors: []string{text("Cross-tenant order lookups return 404 Not Found.")},
		}
	}
	asSent := func(s string) string { return s }

	// The generated encoder refuses a payload that breaks the design, so a
	// refused text is encoded as placeholder and swapped in afterwards by
	// withText to reach the MCP and HTTP decoders as sent.
	const placeholder = "text-under-test"
	withText := func(t *testing.T, args json.RawMessage, text string) json.RawMessage {
		t.Helper()
		quoted, err := json.Marshal(text)
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(args), `"`+placeholder+`"`))
		return json.RawMessage(strings.Replace(string(args), `"`+placeholder+`"`, string(quoted), 1))
	}

	// send calls propose_change or revise_change through entry, the Go method
	// with payload or the MCP tool and HTTP route with args, and reports whether
	// the call was accepted, with the refusal message otherwise.
	send := func(t *testing.T, entry string, payload any, args json.RawMessage) (bool, string) {
		t.Helper()
		var (
			tool, route string
			goErr       error
		)
		switch p := payload.(type) {
		case *genhighbay.ProposeChangePayload:
			tool, route = "propose_change", "/changes/propose"
			if entry == "go" {
				_, goErr = cp.ProposeChange(alice, p)
			}
		case *genhighbay.ReviseChangePayload:
			tool, route = "revise_change", "/changes/revise"
			if entry == "go" {
				_, goErr = cp.ReviseChange(alice, p)
			}
		}
		switch entry {
		case "go":
			if goErr != nil {
				assertServiceError(t, goErr, "unprocessable")
				return false, goErr.Error()
			}
			return true, ""
		case "mcp":
			res, err := adapter.ToolsCall(alice, &genmcpadapter.ToolsCallPayload{Name: tool, Arguments: args})
			require.NoError(t, err)
			complete, ok := res.Outcome.AsComplete()
			require.True(t, ok)
			if complete.IsError == nil || !*complete.IsError {
				return true, ""
			}
			require.NotEmpty(t, complete.Content)
			require.NotNil(t, complete.Content[0].Text)
			assert.Contains(t, *complete.Content[0].Text, "invalid arguments")
			return false, *complete.Content[0].Text
		default:
			status, body := postJSON(t, srv.URL+route, "alice-token", string(args))
			if status == http.StatusOK {
				return true, ""
			}
			assert.Equal(t, http.StatusBadRequest, status, body)
			return false, body
		}
	}
	type (
		// textField is one required analysis text and how to set it in a
		// proposal and in a revision.
		textField struct {
			name    string
			propose func(*genhighbay.CP1Analysis, string)
			revise  func(*genhighbay.CP1RevisionAnalysis, string)
		}
		// blankText is a value made only of whitespace.
		blankText struct{ name, value string }
	)
	fields := []textField{
		{"summary", func(a *genhighbay.CP1Analysis, v string) { a.Summary = v }, func(a *genhighbay.CP1RevisionAnalysis, v string) { a.Summary = v }},
		{"question", func(a *genhighbay.CP1Analysis, v string) { a.Ambiguities[0].Question = v }, func(a *genhighbay.CP1RevisionAnalysis, v string) { a.Ambiguities[0].Question = v }},
		{"options", func(a *genhighbay.CP1Analysis, v string) { a.Ambiguities[0].Options[1] = v }, func(a *genhighbay.CP1RevisionAnalysis, v string) { a.Ambiguities[0].Options[1] = v }},
		{"scenario", func(a *genhighbay.CP1Analysis, v string) { a.Examples[0].Scenario = v }, func(a *genhighbay.CP1RevisionAnalysis, v string) { a.Examples[0].Scenario = v }},
		{"input_summary", func(a *genhighbay.CP1Analysis, v string) { a.Examples[0].InputSummary = v }, func(a *genhighbay.CP1RevisionAnalysis, v string) { a.Examples[0].InputSummary = v }},
		{"expected_outcome", func(a *genhighbay.CP1Analysis, v string) { a.Examples[0].ExpectedOutcome = v }, func(a *genhighbay.CP1RevisionAnalysis, v string) { a.Examples[0].ExpectedOutcome = v }},
		{"preserved_behaviors", func(a *genhighbay.CP1Analysis, v string) { a.PreservedBehaviors[0] = v }, func(a *genhighbay.CP1RevisionAnalysis, v string) { a.PreservedBehaviors[0] = v }},
	}
	blanks := []blankText{
		{"empty", ""},
		{"ASCII space", " "},
		{"ASCII whitespace", " \t\n\r\f"},
		{"vertical tab", "\v"},
		{"no-break space", noBreakSpace},
		{"em space", emSpace},
		{"ideographic space", ideographicSpace},
		{"next line", nextLine},
		{"ASCII and Unicode spaces", " " + noBreakSpace + "\t" + ideographicSpace + "\n" + emSpace},
	}

	// refuse sends a proposal and a revision of the saved card current whose
	// field holds only blank through entry and checks that both are refused
	// for that field.
	current := mustPropose(t, cp, alice, "chg-revise", "Refunds up to $50 are instant.", proposal(asSent))
	refuse := func(t *testing.T, entry string, field textField, blank blankText) {
		t.Helper()
		proposed := proposal(asSent)
		field.propose(proposed, placeholder)
		propose := &genhighbay.ProposeChangePayload{
			ChangeID: "chg-blank", Prompt: "Refunds up to $50 are instant.", Analysis: proposed,
		}
		args := withText(t, proposeArgs(t, propose), blank.value)
		field.propose(proposed, blank.value)
		ok, msg := send(t, entry, propose, args)
		assert.False(t, ok, "propose_change accepted a blank %s", field.name)
		assert.Contains(t, msg, field.name)

		revised := revision(asSent)
		field.revise(revised, placeholder)
		revise := &genhighbay.ReviseChangePayload{
			ChangeID: "chg-revise", RequestRevision: current.RequestRevision,
			Prompt: "Refunds up to $50 are instant.", RevisionNote: "Revise the change.", Analysis: revised,
		}
		args = withText(t, reviseArgs(t, revise), blank.value)
		field.revise(revised, blank.value)
		ok, msg = send(t, entry, revise, args)
		assert.False(t, ok, "revise_change accepted a blank %s", field.name)
		assert.Contains(t, msg, field.name)
	}

	// Every field and kind of whitespace is checked at the Go method, the
	// cheapest entry, which runs the generated validators. The MCP and HTTP
	// decoders run the same validators, so one kind of whitespace per field
	// shows their refusal.
	transportBlank := map[string]string{
		"summary":             "no-break space",
		"options":             "ASCII and Unicode spaces",
		"preserved_behaviors": "next line",
	}
	for _, field := range fields {
		for _, blank := range blanks {
			t.Run("refuses "+field.name+" "+blank.name+" via go", func(t *testing.T) {
				refuse(t, "go", field, blank)
			})
			if transportBlank[field.name] != blank.name {
				continue
			}
			for _, entry := range []string{"mcp", "http"} {
				t.Run("refuses "+field.name+" "+blank.name+" via "+entry, func(t *testing.T) {
					refuse(t, entry, field, blank)
				})
			}
		}
	}
	_, saved, err := cp.cards.load("chg-blank")
	require.NoError(t, err)
	assert.False(t, saved, "no refused proposal saves a card")
	unchanged, err := cp.GetChange(alice, &genhighbay.GetChangePayload{ChangeID: "chg-revise"})
	require.NoError(t, err)
	assert.Equal(t, current.RequestRevision, unchanged.RequestRevision, "no refused revision changes the card")

	// U+200B and U+FEFF are not whitespace (unicode.IsSpace reports false), so
	// a value made of them is text.
	accepted := []struct {
		name       string
		text       func(string) string
		transports bool
	}{
		{"no-break spaces around text", func(s string) string { return noBreakSpace + s + noBreakSpace }, true},
		{"ASCII and Unicode spaces around text", func(s string) string { return " \t" + noBreakSpace + s + ideographicSpace + "\n" }, false},
		{"zero width space", func(string) string { return zeroWidthSpace }, false},
		{"byte order mark", func(string) string { return byteOrderMark }, false},
		{"byte order mark and spaces", func(string) string { return byteOrderMark + " " + noBreakSpace }, false},
	}
	for i, tc := range accepted {
		entries := []string{"go"}
		if tc.transports {
			entries = append(entries, "mcp", "http")
		}
		for _, entry := range entries {
			t.Run("saves "+tc.name+" as sent via "+entry, func(t *testing.T) {
				changeID := fmt.Sprintf("chg-padded-%d-%s", i, entry)
				propose := &genhighbay.ProposeChangePayload{
					ChangeID: changeID, Prompt: "Refunds up to $50 are instant.", Analysis: proposal(tc.text),
				}
				ok, msg := send(t, entry, propose, proposeArgs(t, propose))
				require.True(t, ok, msg)
				proposed, err := cp.GetChange(alice, &genhighbay.GetChangePayload{ChangeID: changeID})
				require.NoError(t, err)
				assertCardTexts(t, proposed, tc.text)

				revise := &genhighbay.ReviseChangePayload{
					ChangeID: changeID, RequestRevision: proposed.RequestRevision,
					Prompt: "Refunds up to $50 are instant, tax included.", RevisionNote: "Revise the change.", Analysis: revision(tc.text),
				}
				ok, msg = send(t, entry, revise, reviseArgs(t, revise))
				require.True(t, ok, msg)
				revised, err := cp.GetChange(alice, &genhighbay.GetChangePayload{ChangeID: changeID})
				require.NoError(t, err)
				assert.Equal(t, "Refunds up to $50 are instant, tax included.", revised.Prompt, "the revision was saved")
				assertCardTexts(t, revised, tc.text)
			})
		}
	}
}

// assertCardTexts checks that card holds, byte for byte, the analysis texts
// TestAnalysisTextsMustHoldMoreThanWhitespace sent through text.
func assertCardTexts(t *testing.T, card *genhighbay.ChangeCardResult, text func(string) string) {
	t.Helper()
	assert.Equal(t, text("Refunds up to $50 are instant."), card.Summary)
	require.Len(t, card.Ambiguities, 1)
	assert.Equal(t, text("Is $50.00 itself instant?"), card.Ambiguities[0].Question)
	assert.Equal(t, []string{"instant", text("manager")}, card.Ambiguities[0].Options)
	require.Len(t, card.Examples, 1)
	assert.Equal(t, "check_01", card.Examples[0].ID)
	assert.Equal(t, text("Refund at the limit"), card.Examples[0].Scenario)
	assert.Equal(t, text("Order total $50.00"), card.Examples[0].InputSummary)
	assert.Equal(t, text("Instant store credit"), card.Examples[0].ExpectedOutcome)
	assert.Equal(t, []string{text("Cross-tenant order lookups return 404 Not Found.")}, card.PreservedBehaviors)
}

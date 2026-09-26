// internal/observe/transcript/detect_test.go
package transcript

import (
	"strings"
	"testing"

	"github.com/BrutalSystems/birddog/internal/policy"
)

func ask(id, question string) Record {
	return Record{Type: "assistant", Role: "assistant", Parts: []Part{{
		Type: "tool_use", Name: "AskUserQuestion", ID: id,
		Input: []byte(`{"questions":[{"question":"` + question + `"}]}`),
	}}}
}

func result(id string) Record {
	return Record{Type: "user", Role: "user", Parts: []Part{{Type: "tool_result", ToolUseID: id}}}
}

func says(text string) Record {
	return Record{Type: "assistant", Role: "assistant", Parts: []Part{{Type: "text", Text: text}}}
}

func TestDetectFindsAnUnansweredAskUserQuestion(t *testing.T) {
	got := Detect([]Record{says("working"), ask("t1", "Which approach do you want?")}, false)
	if got == nil {
		t.Fatal("Detect = nil, want a question")
	}
	if got.Kind != policy.RequestKindQuestion {
		t.Errorf("Kind = %q, want %q", got.Kind, policy.RequestKindQuestion)
	}
	if got.Detail != "Which approach do you want?" {
		t.Errorf("Detail = %q", got.Detail)
	}
}

func TestDetectIgnoresAnAnsweredAskUserQuestion(t *testing.T) {
	if got := Detect([]Record{ask("t1", "Which?"), result("t1")}, false); got != nil {
		t.Errorf("Detect = %+v, want nil", got)
	}
}

// Review Focus 3: the turn moved on without the question being answered.
func TestDetectIgnoresAnAbandonedAskUserQuestion(t *testing.T) {
	recs := []Record{ask("t1", "Which?"), says("never mind, carrying on"), says("done")}
	if got := Detect(recs, true); got != nil && got.Kind == policy.RequestKindQuestion {
		t.Errorf("Detect = %+v, want no exact question", got)
	}
}

func TestDetectInfersAQuestionFromTrailingText(t *testing.T) {
	got := Detect([]Record{says("Want me to fix the table, or split the file?")}, true)
	if got == nil {
		t.Fatal("Detect = nil, want an inferred question")
	}
	if got.Kind != policy.RequestKindQuestionInferred {
		t.Errorf("Kind = %q, want %q", got.Kind, policy.RequestKindQuestionInferred)
	}
	if got.Detail != "Want me to fix the table, or split the file?" {
		t.Errorf("Detail = %q", got.Detail)
	}
}

func TestDetectDoesNotInferFromAStatement(t *testing.T) {
	if got := Detect([]Record{says("Understood — holding. Ping me when they wrap up.")}, true); got != nil {
		t.Errorf("Detect = %+v, want nil", got)
	}
}

// The status guard: measured, not defensive. Every busy session sampled ended
// on a tool call; a trailing "?" under busy is mid-turn prose.
func TestDetectDoesNotInferWhenNotIdle(t *testing.T) {
	if got := Detect([]Record{says("Shall I continue?")}, false); got != nil {
		t.Errorf("Detect = %+v, want nil when not idle", got)
	}
}

// The exact signal needs no corroboration from status.
func TestDetectFindsAnExactQuestionEvenWhenNotIdle(t *testing.T) {
	if got := Detect([]Record{ask("t1", "Which?")}, false); got == nil {
		t.Error("Detect = nil, want the exact question regardless of status")
	}
}

// Review Focus 4: a subagent's words are not the session's question.
func TestDetectSkipsSidechainRecords(t *testing.T) {
	r := says("Shall I continue?")
	r.IsSidechain = true
	if got := Detect([]Record{r}, true); got != nil {
		t.Errorf("Detect = %+v, want nil for a sidechain record", got)
	}
}

func TestDetectReturnsNilOnAnEmptyWindow(t *testing.T) {
	if got := Detect(nil, true); got != nil {
		t.Errorf("Detect = %+v, want nil", got)
	}
}

func TestDetectUsesOnlyTheTrailingSentence(t *testing.T) {
	got := Detect([]Record{says("I read the file. It has three problems. Which should I fix first?")}, true)
	if got == nil || got.Detail != "Which should I fix first?" {
		t.Errorf("Detail = %+v, want the trailing sentence only", got)
	}
}

// 26 of 110 real AskUserQuestion calls on this machine ask more than one
// question. Reporting only the first leaves an operator answering part of
// what was asked and the session still blocked, with nothing saying so.
func TestDetectReportsEveryQuestionInACall(t *testing.T) {
	r := Record{Type: "assistant", Role: "assistant", Parts: []Part{{
		Type: "tool_use", Name: "AskUserQuestion", ID: "t1",
		Input: []byte(`{"questions":[{"question":"Which backoff?"},{"question":"How many attempts?"}]}`),
	}}}

	got := Detect([]Record{r}, false)
	if got == nil {
		t.Fatal("Detect = nil, want a question")
	}
	for _, want := range []string{"Which backoff?", "How many attempts?"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("Detail = %q, missing %q", got.Detail, want)
		}
	}
}

// The count leads, because the detail is bounded at 200 runes before it
// reaches the durable record: with two long questions the second is cut, and
// the only thing that can tell an operator it existed is a prefix short
// enough to survive. Real calls reach 481 runes combined.
func TestDetectLeadsWithTheQuestionCountWhenThereIsMoreThanOne(t *testing.T) {
	long := strings.Repeat("a very long question indeed ", 12)
	r := Record{Type: "assistant", Role: "assistant", Parts: []Part{{
		Type: "tool_use", Name: "AskUserQuestion", ID: "t1",
		Input: []byte(`{"questions":[{"question":"` + long + `?"},{"question":"` + long + `?"}]}`),
	}}}

	got := Detect([]Record{r}, false)
	if got == nil {
		t.Fatal("Detect = nil")
	}
	if !strings.HasPrefix(got.Detail, "2 questions: ") {
		t.Errorf("Detail does not lead with the count: %.40q", got.Detail)
	}
	if bounded := policy.BoundDetail(got.Detail); !strings.HasPrefix(bounded, "2 questions: ") {
		t.Errorf("the count did not survive bounding: %.40q", bounded)
	}
}

// One question is the common case (84 of 110) and must stay clean.
func TestDetectDoesNotCountASingleQuestion(t *testing.T) {
	got := Detect([]Record{ask("t1", "Which backoff?")}, false)
	if got == nil {
		t.Fatal("Detect = nil")
	}
	if got.Detail != "Which backoff?" {
		t.Errorf("Detail = %q, want the bare question", got.Detail)
	}
}

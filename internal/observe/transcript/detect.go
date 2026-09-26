// internal/observe/transcript/detect.go
package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BrutalSystems/birddog/internal/policy"
)

// Question is a pending question found in a transcript.
type Question struct {
	// Kind is policy.RequestKindQuestion for a question the session is
	// provably blocked on, or policy.RequestKindQuestionInferred for one
	// read from the shape of the conversation.
	Kind string

	// Detail is the question itself, where it could be read.
	Detail string
}

// askTool is the tool whose pending call is an exact question signal.
const askTool = "AskUserQuestion"

// Detect reports a question the session is waiting on, or nil.
//
// Two signals, kept apart because they are not equally reliable:
//
//   - An AskUserQuestion tool call with no matching tool_result, and nothing
//     after it, is exact: the turn cannot proceed until a human answers. The
//     pairing was verified over seven answered calls in one session.
//   - A trailing assistant message ending in "?" is a heuristic. A rhetorical
//     closing question is indistinguishable from a real one, so it is reported
//     under its own kind and only when the session is otherwise idle.
//
// idle is what the inner adapter reported. It guards only the inferred signal:
// every busy session sampled ended on a tool call, so a trailing "?" under
// busy is prose mid-turn. The exact signal needs no such corroboration.
func Detect(recs []Record, idle bool) *Question {
	answered := map[string]bool{}
	for _, r := range recs {
		if r.IsSidechain {
			continue
		}
		for _, p := range r.Parts {
			if p.Type == "tool_result" && p.ToolUseID != "" {
				answered[p.ToolUseID] = true
			}
		}
	}

	// Walk backwards to the newest message that settles the question.
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if r.IsSidechain {
			continue
		}
		for _, p := range r.Parts {
			if p.Type == "tool_use" && p.Name == askTool && !answered[p.ID] {
				return &Question{Kind: policy.RequestKindQuestion, Detail: askText(p.Input)}
			}
		}
		// The newest non-sidechain message was not a pending ask. Anything
		// older is behind a turn that continued, so it is not pending
		// either — an abandoned question is not an open one.
		if len(r.Parts) == 0 {
			continue
		}
		if !idle || r.Role != "assistant" {
			return nil
		}
		text := trailingText(r)
		if s := lastSentence(text); strings.HasSuffix(s, "?") {
			return &Question{Kind: policy.RequestKindQuestionInferred, Detail: s}
		}
		return nil
	}
	return nil
}

// askText pulls the questions out of an AskUserQuestion call.
//
// A call can carry more than one — 26 of 110 real calls sampled on one machine
// did, one of them three. Reporting only the first leaves an operator
// answering part of what was asked, with the session still blocked and
// nothing saying why.
//
// Where there is more than one, the count leads. The detail is bounded at
// policy.MaxDetail runes before it reaches the durable record and real calls
// reach 481 runes combined, so the later questions are often cut; a short
// prefix is the only part guaranteed to survive, and "2 questions:" is what
// tells a reader that what follows is incomplete.
func askText(input json.RawMessage) string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Questions) == 0 {
		// The call is still pending whether or not its arguments can be
		// read. Reporting the block with no detail beats reporting nothing.
		return ""
	}

	asked := make([]string, 0, len(in.Questions))
	for _, q := range in.Questions {
		if q.Question != "" {
			asked = append(asked, q.Question)
		}
	}
	switch len(asked) {
	case 0:
		return ""
	case 1:
		return asked[0]
	default:
		return fmt.Sprintf("%d questions: %s", len(asked), strings.Join(asked, " | "))
	}
}

// trailingText is the text of a message's last text part.
func trailingText(r Record) string {
	for i := len(r.Parts) - 1; i >= 0; i-- {
		if r.Parts[i].Type == "text" {
			return r.Parts[i].Text
		}
	}
	return ""
}

// lastSentence is the final sentence of a message, which is where a question
// to the human sits when there is one.
func lastSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Only split on a terminator that ends a sentence, so "3.5 seconds?" and
	// "the U.S. rate?" are not cut in half by their own punctuation.
	cut := -1
	runes := []rune(text)
	for i := 0; i < len(runes)-1; i++ {
		switch runes[i] {
		case '.', '!', '?':
			if runes[i+1] == ' ' || runes[i+1] == '\n' {
				cut = i
			}
		}
	}
	if cut < 0 {
		return text
	}
	return strings.TrimSpace(string(runes[cut+1:]))
}

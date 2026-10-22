package llm

import "context"

// DecisionKind is the answer type of a decision question.
type DecisionKind string

const (
	// DecisionNoul is a yes/no proposition, answered with the probability of yes.
	DecisionNoul DecisionKind = "noul"
	// DecisionChoice picks one named option.
	DecisionChoice DecisionKind = "choice"
	// DecisionScore places the state on an ordered scale of levels.
	DecisionScore DecisionKind = "score"
	// DecisionBox locates a described target in an image of the state.
	DecisionBox DecisionKind = "box"
)

// DecisionRequest asks a decision model (Jev and friends) typed questions
// about one state. A decision model returns calibrated, structured answers to
// branch on instead of prose, and bills input tokens only.
type DecisionRequest struct {
	Model string
	// State is the content to judge: a string, or any JSON-marshalable map or
	// slice of related context. Images go into a []any state as ImagePart
	// values next to text, e.g. []any{"The listing photo:", ImagePart{URL: u}};
	// Data without URL is sent as a data URI.
	State any
	// Questions are keyed by a name of the caller's choosing; Answers use the
	// same keys.
	Questions map[string]DecisionQuestion
}

// DecisionQuestion is one typed question. Build it with NoulQuestion,
// ChoiceQuestion, ScoreQuestion or BoxQuestion.
type DecisionQuestion struct {
	Kind         DecisionKind
	Instructions string
	// Options are the criteria of a choice (option name to description,
	// required) or of a noul (optional, keys "true" and "false" together).
	Options map[string]string
	// Levels are the criteria of a score, ordered from the first (index 0)
	// to the last. Required for a score.
	Levels []string
}

// NoulQuestion is a yes/no question.
func NoulQuestion(instructions string) DecisionQuestion {
	return DecisionQuestion{Kind: DecisionNoul, Instructions: instructions}
}

// ChoiceQuestion picks one of options (name to description).
func ChoiceQuestion(instructions string, options map[string]string) DecisionQuestion {
	return DecisionQuestion{Kind: DecisionChoice, Instructions: instructions, Options: options}
}

// ScoreQuestion places the state on levels, ordered first to last.
func ScoreQuestion(instructions string, levels ...string) DecisionQuestion {
	return DecisionQuestion{Kind: DecisionScore, Instructions: instructions, Levels: levels}
}

// BoxQuestion locates a target in the state's image. A box is returned even
// when the target is absent: pair it with a NoulQuestion on presence.
func BoxQuestion(instructions string) DecisionQuestion {
	return DecisionQuestion{Kind: DecisionBox, Instructions: instructions}
}

// DecisionResponse holds one answer per question.
type DecisionResponse struct {
	Answers map[string]DecisionAnswer
	// Model is the dated model that answered ("typesafe/jev-1.14-20261014").
	Model string
	// ID is the generation id ("gen-dec-...").
	ID string
	// Usage has the tokens, CostUSD, Provider and the call's timing.
	Usage *Usage
}

// DecisionAnswer is one answer; which fields are set depends on Kind.
type DecisionAnswer struct {
	Kind DecisionKind
	// Noul is the probability of yes, 0 to 1 (noul).
	Noul float64
	// Choice is the picked option name (choice).
	Choice string
	// Score is the probability-weighted position on the levels, 0 being the
	// first (score).
	Score float64
	// Probabilities maps each option name (choice) or level index "0", "1",
	// ... (score) to its probability.
	Probabilities map[string]float64
	// Legend maps the level indices to their levels (score).
	Legend map[string]string
	// Confidence is how concentrated the distribution is, 0 to 1 (choice,
	// score, box).
	Confidence float64
	// Box is the located target (box).
	Box *DecisionBoxArea
}

// DecisionBoxArea is a bounding box in fractions of the image height (Top,
// Bottom) and width (Left, Right), measured from the top-left corner.
type DecisionBoxArea struct {
	Top, Bottom, Left, Right float64
}

// Decider is implemented by clients that speak a decisions endpoint. It is an
// optional capability, not part of Client: callers type-assert on it.
// Implementations map provider errors onto the same Err* sentinels Complete
// uses.
type Decider interface {
	Decide(ctx context.Context, req DecisionRequest) (DecisionResponse, error)
}

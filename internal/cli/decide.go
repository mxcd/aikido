package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/mxcd/aikido/llm"

	urfavecli "github.com/urfave/cli/v3"
)

// defaultDecisionModel tracks TypeSafe's newest Jev release.
const defaultDecisionModel = "~typesafe/jev-latest"

func decideCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "decide",
		Usage:     "Ask a decision model (Jev, ...) typed questions about a state",
		ArgsUsage: "<state...>  (or the state on stdin)",
		Description: "Sends the state and the questions to OpenRouter's Decisions API and prints one\n" +
			"answer per question. Questions are NAME=INSTRUCTIONS, choice and score add\n" +
			"=OPT1,OPT2,... (score levels ordered first to last):\n\n" +
			"  aikido decide --noul 'is_bug=Is this a bug report?' \\\n" +
			"    --choice 'team=Which team owns it?=payments,frontend,account' \\\n" +
			"    --score 'urgency=How urgent?=can wait,this week,blocking revenue' \\\n" +
			"    'Checkout shows a blank page after I click Pay.'\n\n" +
			"Decision models: `aikido models --decisions`.",
		DisableSliceFlagSeparator: true,
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{
				Name:    "model",
				Aliases: []string{"m"},
				Value:   defaultDecisionModel,
				Sources: urfavecli.EnvVars("OPENROUTER_DECISION_MODEL"),
				Usage:   "OpenRouter decision model id",
			},
			&urfavecli.StringSliceFlag{Name: "noul", Usage: "Yes/no question NAME=INSTRUCTIONS; repeatable"},
			&urfavecli.StringSliceFlag{Name: "choice", Usage: "Choice question NAME=INSTRUCTIONS=OPT1,OPT2,...; repeatable"},
			&urfavecli.StringSliceFlag{Name: "score", Usage: "Score question NAME=INSTRUCTIONS=LEVEL1,LEVEL2,...; repeatable"},
			&urfavecli.StringSliceFlag{Name: "box", Usage: "Bounding-box question on --image NAME=INSTRUCTIONS; repeatable"},
			&urfavecli.StringSliceFlag{Name: "image", Usage: "Image file added to the state; repeatable"},
			providerFlag,
		},
		Action: func(ctx context.Context, cmd *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			questions, err := parseQuestions(cmd.StringSlice("noul"), cmd.StringSlice("choice"), cmd.StringSlice("score"), cmd.StringSlice("box"))
			if err != nil {
				return err
			}
			text := strings.TrimSpace(strings.Join(cmd.Args().Slice(), " "))
			if text == "" || text == "-" {
				b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
				if err != nil {
					return fmt.Errorf("read state from stdin: %w", err)
				}
				text = strings.TrimSpace(string(b))
			}
			images, err := loadReferenceImages(cmd.StringSlice("image"))
			if err != nil {
				return err
			}
			req := llm.DecisionRequest{Model: cmd.String("model"), State: text, Questions: questions}
			if len(images) > 0 {
				state := []any{}
				if text != "" {
					state = append(state, text)
				}
				for _, img := range images {
					state = append(state, img)
				}
				req.State = state
			} else if text == "" {
				return errors.New("missing state: pass it as arguments, on stdin, or as --image")
			}
			return runDecide(withProviderFlag(ctx, cmd), req, resolveWriter(cmd), client)
		},
	}
}

// parseQuestions turns the --noul/--choice/--score/--box values into questions.
func parseQuestions(nouls, choices, scores, boxes []string) (map[string]llm.DecisionQuestion, error) {
	qs := map[string]llm.DecisionQuestion{}
	add := func(flag, v string, withList bool, build func(instr string, list []string) llm.DecisionQuestion) error {
		name, rest, ok := strings.Cut(v, "=")
		name, rest = strings.TrimSpace(name), strings.TrimSpace(rest)
		var list []string
		if ok && withList {
			i := strings.LastIndex(rest, "=")
			if i < 0 {
				ok = false
			} else {
				for _, it := range strings.Split(rest[i+1:], ",") {
					if it = strings.TrimSpace(it); it != "" {
						list = append(list, it)
					}
				}
				rest = strings.TrimSpace(rest[:i])
			}
		}
		if !ok || name == "" || rest == "" || (withList && len(list) == 0) {
			want := "NAME=INSTRUCTIONS"
			if withList {
				want += "=A,B,..."
			}
			return fmt.Errorf("--%s %q: want %s", flag, v, want)
		}
		if _, dup := qs[name]; dup {
			return fmt.Errorf("question name %q used twice", name)
		}
		qs[name] = build(rest, list)
		return nil
	}
	for _, set := range []struct {
		flag     string
		values   []string
		withList bool
		build    func(string, []string) llm.DecisionQuestion
	}{
		{"noul", nouls, false, func(i string, _ []string) llm.DecisionQuestion { return llm.NoulQuestion(i) }},
		{"choice", choices, true, func(i string, l []string) llm.DecisionQuestion {
			opts := make(map[string]string, len(l))
			for _, o := range l {
				opts[o] = o
			}
			return llm.ChoiceQuestion(i, opts)
		}},
		{"score", scores, true, func(i string, l []string) llm.DecisionQuestion { return llm.ScoreQuestion(i, l...) }},
		{"box", boxes, false, func(i string, _ []string) llm.DecisionQuestion { return llm.BoxQuestion(i) }},
	} {
		for _, v := range set.values {
			if err := add(set.flag, v, set.withList, set.build); err != nil {
				return nil, err
			}
		}
	}
	if len(qs) == 0 {
		return nil, errors.New("no questions: add --noul, --choice, --score or --box")
	}
	return qs, nil
}

func runDecide(ctx context.Context, req llm.DecisionRequest, out io.Writer, client llm.Client) error {
	d, ok := client.(llm.Decider)
	if !ok {
		return errors.New("client does not support the Decisions API")
	}
	resp, err := d.Decide(ctx, req)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, name := range slices.Sorted(maps.Keys(resp.Answers)) {
		a := resp.Answers[name]
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, a.Kind, formatAnswer(a))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if resp.Usage != nil {
		fmt.Fprintf(out, "\n%s model=%s\n", formatUsage(resp.Usage), resp.Model)
	}
	return nil
}

func formatAnswer(a llm.DecisionAnswer) string {
	switch a.Kind {
	case llm.DecisionNoul:
		return fmt.Sprintf("%.2f", a.Noul)
	case llm.DecisionChoice:
		return fmt.Sprintf("%s (confidence %.2f; %s)", a.Choice, a.Confidence, formatProbs(a.Probabilities, nil))
	case llm.DecisionScore:
		return fmt.Sprintf("%.2f (confidence %.2f; %s)", a.Score, a.Confidence, formatProbs(a.Probabilities, a.Legend))
	case llm.DecisionBox:
		if a.Box == nil {
			return "-"
		}
		return fmt.Sprintf("top=%.2f bottom=%.2f left=%.2f right=%.2f (confidence %.2f)", a.Box.Top, a.Box.Bottom, a.Box.Left, a.Box.Right, a.Confidence)
	}
	return ""
}

// formatProbs lists probabilities high to low, keys renamed through legend.
func formatProbs(p map[string]float64, legend map[string]string) string {
	keys := slices.Collect(maps.Keys(p))
	slices.SortFunc(keys, func(a, b string) int {
		if p[a] != p[b] {
			if p[a] > p[b] {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		label := k
		if l, ok := legend[k]; ok {
			label = l
		}
		parts[i] = fmt.Sprintf("%s %.2f", label, p[k])
	}
	return strings.Join(parts, ", ")
}

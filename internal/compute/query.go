package compute

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sourcegraph/sourcegraph/internal/search/query"

	"github.com/cockroachdb/errors"
)

type Query struct {
	Command    Command
	Parameters []query.Parameter
}

func (q Query) String() string {
	return fmt.Sprintf("Command: `%s`, Parameters: `%s`",
		q.Command.String(),
		query.Q(query.ToNodes(q.Parameters)).String())
}

type Command interface {
	command()
	String() string
}

func (MatchOnly) command()            {}
func (ReplaceInPlace) command()       {}
func (ReplaceWithSeparator) command() {}

type MatchOnly struct {
	MatchPattern MatchPattern
}

type ReplaceInPlace struct {
	MatchPattern   MatchPattern
	ReplacePattern string
}

type ReplaceWithSeparator struct {
	MatchPattern   MatchPattern
	ReplacePattern string
	Separator      string
}

func (c MatchOnly) String() string {
	return fmt.Sprintf("Match only: %s", c.MatchPattern.String())
}

func (c ReplaceInPlace) String() string {
	return fmt.Sprintf("Replace in place: %s -> %s", c.MatchPattern.String(), c.ReplacePattern)
}

func (c ReplaceWithSeparator) String() string {
	return fmt.Sprintf("Replace with separator: %s -> %s separator: %s", c.MatchPattern.String(), c.ReplacePattern, c.Separator)
}

type MatchPattern interface {
	pattern()
	String() string
}

func (Regexp) pattern() {}
func (Comby) pattern()  {}

type Regexp struct {
	Value *regexp.Regexp
}

type Comby struct {
	Value string
}

func (p Regexp) String() string {
	return p.Value.String()
}

func (p Comby) String() string {
	return p.Value
}

func extractPattern(basic query.Basic) (*query.Pattern, error) {
	if basic.Pattern == nil {
		return nil, errors.New("compute endpoint expects nonempty pattern")
	}
	var err error
	var pattern *query.Pattern
	seen := false
	query.VisitPattern([]query.Node{basic.Pattern}, func(value string, negated bool, annotation query.Annotation) {
		if err != nil {
			return
		}
		if negated {
			err = errors.New("compute endpoint expects a nonnegated pattern")
			return
		}
		if seen {
			err = errors.New("compute endpoint only supports one search pattern currently ('and' or 'or' operators are not supported yet)")
			return
		}
		pattern = &query.Pattern{Value: value, Annotation: annotation}
		seen = true
	})
	if err != nil {
		return nil, err
	}
	return pattern, nil
}

func toRegexpPattern(value string) (*Regexp, error) {
	rp, err := regexp.Compile(value)
	if err != nil {
		return nil, errors.Wrap(err, "compute endpoint")
	}
	return &Regexp{Value: rp}, nil
}

var ComputePredicateRegistry = query.PredicateRegistry{
	query.FieldContent: {
		"replace": func() query.Predicate { return query.EmptyPredicate{} },
	},
}

func parseReplaceInPlace(pattern *query.Pattern) (*ReplaceInPlace, bool, error) {
	if !pattern.Annotation.Labels.IsSet(query.IsAlias) {
		// pattern is not set via `content:`, so it cannot be a replace command.
		return nil, false, nil
	}
	value, _, ok := query.ScanPredicate("content", []byte(pattern.Value), ComputePredicateRegistry)
	if !ok {
		return nil, false, nil
	}
	_, args := query.ParseAsPredicate(value)
	parts := strings.Split(args, "->")
	if len(parts) != 2 {
		return nil, false, errors.New("invalid replace statement, no left and right hand sides of `->`")
	}
	rp, err := toRegexpPattern(parts[0])
	if err != nil {
		return nil, false, errors.Wrap(err, "replace command")
	}
	return &ReplaceInPlace{MatchPattern: rp, ReplacePattern: parts[1]}, true, nil
}

func toCommand(pattern *query.Pattern) (Command, error) {
	command, ok, err := parseReplaceInPlace(pattern)
	if err != nil {
		return nil, err
	}
	if ok {
		return command, nil
	}

	rp, err := toRegexpPattern(pattern.Value)
	if err != nil {
		return nil, err
	}
	return &MatchOnly{MatchPattern: rp}, nil
}

func toComputeQuery(plan query.Plan) (*Query, error) {
	if len(plan) != 1 {
		return nil, errors.New("compute endpoint only supports one search pattern currently ('and' or 'or' operators are not supported yet)")
	}
	pattern, err := extractPattern(plan[0])
	if err != nil {
		return nil, err
	}
	command, err := toCommand(pattern)
	if err != nil {
		return nil, err
	}
	return &Query{
		Parameters: plan[0].Parameters,
		Command:    command,
	}, nil
}

func Parse(q string) (*Query, error) {
	plan, err := query.Pipeline(query.Init(q, query.SearchTypeRegex))
	if err != nil {
		return nil, err
	}
	return toComputeQuery(plan)
}

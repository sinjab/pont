package cli

// gh-style --jq (P4, rewrite-consult Oct 11 2026): run a gojq
// expression over the JSON output. Applies AFTER --fields filtering,
// BEFORE rendering:
//
//	--format json  → the query runs over the whole payload (array or
//	                 object), like gh:  pont chats --jq '.[].name'
//	--format jsonl → the query runs per element (each line's object
//	                 is the input ".")
//
// Results print one per line: strings bare (grep-able), everything
// else as compact JSON. Non-JSON formats reject --jq explicitly —
// silent no-ops are worse than errors.
import (
	"encoding/json"
	"fmt"

	"github.com/itchyny/gojq"
)

// jqExprValid reports whether --jq can apply to this format.
func jqExprValid(f Format) bool {
	return f == FormatJSON || f == FormatJSONL
}

// runJQ applies the gojq expression to data and prints the results.
// The caller decides the input scope: the whole payload (json format)
// or one element at a time (jsonl format).
func runJQ(expr string, data any) error {
	query, err := gojq.Parse(expr)
	if err != nil {
		return fmt.Errorf("--jq: %w", err)
	}

	// gojq operates on plain JSON values (map[string]any / []any):
	// round-trip typed structs through encoding/json first.
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("--jq: marshal input: %w", err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("--jq: unmarshal input: %w", err)
	}

	iter := query.Run(v)
	for {
		res, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, isErr := res.(error); isErr {
			return fmt.Errorf("--jq: %w", err)
		}
		if res == nil {
			fmt.Println("null")
			continue
		}
		if s, isStr := res.(string); isStr {
			// Bare strings: grep-able lines, gh-style.
			fmt.Println(s)
			continue
		}
		line, err := json.Marshal(res)
		if err != nil {
			return fmt.Errorf("--jq: marshal result: %w", err)
		}
		fmt.Println(string(line))
	}
}

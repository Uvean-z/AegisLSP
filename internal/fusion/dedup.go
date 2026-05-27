package fusion

import (
	"regexp"
	"strings"

	"github.com/Uvean-z/aegislsp/internal/sandbox"
	"github.com/Uvean-z/aegislsp/internal/types"
)

// Deduplicator folds highly similar compiler errors to reduce noise.
// Errors are grouped by file, then consecutive errors with the same
// normalized message pattern are collapsed into a single entry with
// a Count > 1.
type Deduplicator interface {
	// Dedup takes a slice of ErrorEntry and returns a reduced slice
	// where consecutive similar errors in the same file are folded.
	// The returned counts parallel the returned entries: counts[i] is
	// the number of original errors that were folded into entries[i].
	Dedup(errors []types.ErrorEntry) (entries []types.ErrorEntry, counts []int)
}

// normRule is a compiled normalization rule.
type normRule struct {
	re          *regexp.Regexp
	replacement string
}

// langNormSet holds compiled normalization rules and keywords for one language.
type langNormSet struct {
	rules    []normRule
	keywords map[string]bool
}

// deduplicatorImpl implements Deduplicator with config-driven language patterns.
type deduplicatorImpl struct {
	langConfigs map[string]langNormSet
}

// NewDeduplicator returns a Deduplicator with generic-only normalization.
// No language-specific patterns are applied. For language-aware dedup,
// use NewDeduplicatorWithConfig.
func NewDeduplicator() Deduplicator {
	return &deduplicatorImpl{langConfigs: make(map[string]langNormSet)}
}

// NewDeduplicatorWithConfig returns a Deduplicator that uses the provided
// configuration for language-specific normalization patterns.
func NewDeduplicatorWithConfig(cfg *sandbox.DedupConfig) Deduplicator {
	langs := make(map[string]langNormSet)
	if cfg != nil {
		for _, lc := range cfg.Languages {
			rules := make([]normRule, 0, len(lc.Rules))
			for _, r := range lc.Rules {
				re, err := regexp.Compile(r.Regex)
				if err != nil {
					// Skip invalid regex — log warning would be ideal,
					// but deduplicator has no logger. Silently skip.
					continue
				}
				rules = append(rules, normRule{re: re, replacement: r.Replacement})
			}
			kw := make(map[string]bool, len(lc.Keywords))
			for _, k := range lc.Keywords {
				kw[k] = true
			}
			langs[lc.Language] = langNormSet{rules: rules, keywords: kw}
		}
	}
	return &deduplicatorImpl{langConfigs: langs}
}

// Generic regexes — language-agnostic, always applied.
var (
	quotedStrRe  = regexp.MustCompile(`"[^"]*"`)
	typeAnnotRe  = regexp.MustCompile(`\([^)]*\)`)
	identRe      = regexp.MustCompile(`\b[a-zA-Z_]\w*\b`)
	numLiteralRe = regexp.MustCompile(`\b\d+\b`)
	multiSpaceRe = regexp.MustCompile(`\s+`)
)

// normalizeMessage strips variable parts from a compiler error message
// to produce a canonical pattern suitable for similarity comparison.
// If a langNormSet is provided, its language-specific rules are applied first.
func normalizeMessage(msg string, lang langNormSet) string {
	s := strings.ToLower(msg)

	// Apply language-specific pattern replacements first.
	for _, rule := range lang.rules {
		s = rule.re.ReplaceAllString(s, rule.replacement)
	}

	// Generic replacements for remaining variable parts.
	s = quotedStrRe.ReplaceAllString(s, `""`)
	s = typeAnnotRe.ReplaceAllString(s, "()")
	s = numLiteralRe.ReplaceAllString(s, "0")

	// Replace remaining identifiers with a placeholder.
	// If language keywords are configured, keep those as-is.
	s = identRe.ReplaceAllStringFunc(s, func(m string) string {
		if lang.keywords[m] {
			return m
		}
		return "VAR"
	})

	s = multiSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// Dedup groups errors by file and folds consecutive entries with the same
// normalized message pattern into a single entry. It returns the deduplicated
// entries and a parallel counts slice where counts[i] is the number of original
// errors folded into entries[i]. Errors from different files or with different
// normalized messages are never folded together.
func (d *deduplicatorImpl) Dedup(errors []types.ErrorEntry) ([]types.ErrorEntry, []int) {
	if len(errors) == 0 {
		return nil, nil
	}

	// Group errors by file, preserving original order within each group.
	type fileGroup struct {
		entries []types.ErrorEntry
		indices []int // original index for stable ordering
	}
	groups := make(map[string]*fileGroup)
	fileOrder := make([]string, 0, 4) // preserve first-seen file order

	for _, e := range errors {
		fg, ok := groups[e.File]
		if !ok {
			fg = &fileGroup{}
			groups[e.File] = fg
			fileOrder = append(fileOrder, e.File)
		}
		fg.entries = append(fg.entries, e)
	}

	// Within each file group, fold consecutive errors with the same
	// normalized message.
	var (
		result []types.ErrorEntry
		counts []int
	)

	for _, file := range fileOrder {
		fg := groups[file]
		for _, e := range fg.entries {
			lang := d.langConfigs[e.Language]
			norm := normalizeMessage(e.Message, lang)
			if len(result) > 0 {
				last := result[len(result)-1]
				if last.File == e.File {
					lastLang := d.langConfigs[last.Language]
					if normalizeMessage(last.Message, lastLang) == norm {
						// Fold into previous entry.
						counts[len(counts)-1]++
						continue
					}
				}
			}
			result = append(result, e)
			counts = append(counts, 1)
		}
	}

	return result, counts
}

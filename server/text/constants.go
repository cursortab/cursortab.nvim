package text

const (
	// SimilarityThreshold is the minimum similarity score for considering
	// two lines as corresponding (modification vs addition/deletion).
	// Below this threshold, lines are treated as unrelated.
	SimilarityThreshold = 0.3

	// ComplexModWordCountThreshold is the maximum word count (per side) for a
	// modification to be considered simple enough for character-level diffing.
	ComplexModWordCountThreshold = 2

	// MaxWordCountDifference is the maximum difference in word count between
	// deleted and inserted text for character-level change classification.
	MaxWordCountDifference = 1

	// MinLengthForEmptyDeletion is the minimum insertion length to treat
	// an empty-deletion + insertion as a full modification rather than addition.
	MinLengthForEmptyDeletion = 10

	// SingleWordMaxRatio and SingleWordMinRatio are the length ratio bounds
	// for single-word replacements. Outside these bounds, the change is complex.
	SingleWordMaxRatio = 3.0
	SingleWordMinRatio = 0.33

	// MultiWordMaxRatio and MultiWordMinRatio are the length ratio bounds
	// for multi-word replacements. Stricter than single-word bounds.
	MultiWordMaxRatio = 2.0
	MultiWordMinRatio = 0.5
)

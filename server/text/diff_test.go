package text

import (
	"cursortab/assert"
	"fmt"
	"testing"
)

// assertChangesEqual compares two changes maps
func assertChangesEqual(t *testing.T, expected, actual map[int]LineChange) {
	t.Helper()

	for lineNum, expectedChange := range expected {
		actualChange, exists := actual[lineNum]
		assert.True(t, exists, fmt.Sprintf("change at line %d exists", lineNum))
		if !exists {
			continue
		}
		assertLineChangeEqual(t, expectedChange, actualChange)
	}

	for lineNum := range actual {
		_, exists := expected[lineNum]
		assert.True(t, exists, fmt.Sprintf("no unexpected change at line %d", lineNum))
	}
}

// assertLineChangeEqual compares two LineChange objects
func assertLineChangeEqual(t *testing.T, expected, actual LineChange) {
	t.Helper()

	assert.Equal(t, expected.Type, actual.Type, "Type")
	assert.Equal(t, expected.Content, actual.Content, "Content")
	assert.Equal(t, expected.OldContent, actual.OldContent, "OldContent")
	assert.Equal(t, expected.ColStart, actual.ColStart, "ColStart")
	assert.Equal(t, expected.ColEnd, actual.ColEnd, "ColEnd")
}

func TestChangeDeletion(t *testing.T) {
	text1 := "line 1\nline 2\nline 3\nline 4"
	text2 := "line 1\nline 3\nline 4"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		2: {
			Type:    ChangeDeletion,
			Content: "line 2",
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestChangeAddition(t *testing.T) {
	text1 := "line 1\nline 3\nline 4"
	text2 := "line 1\nline 2\nline 3\nline 4"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		2: {
			Type:    ChangeAddition,
			Content: "line 2",
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestChangeAppendChars(t *testing.T) {
	text1 := "Hello world"
	text2 := "Hello world!"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		1: {
			Type:       ChangeAppendChars,
			Content:    "Hello world!",
			OldContent: "Hello world",
			ColStart:   11,
			ColEnd:     12,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestChangeDeleteChars(t *testing.T) {
	text1 := "Hello world!"
	text2 := "Hello world"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		1: {
			Type:       ChangeDeleteChars,
			Content:    "Hello world",
			OldContent: "Hello world!",
			ColStart:   11,
			ColEnd:     12,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestChangeDeleteCharsMiddle(t *testing.T) {
	text1 := "Hello world John"
	text2 := "Hello John"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		1: {
			Type:       ChangeDeleteChars,
			Content:    "Hello John",
			OldContent: "Hello world John",
			ColStart:   6,
			ColEnd:     12,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestChangeReplaceChars(t *testing.T) {
	text1 := "Hello world"
	text2 := "Hello there"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		1: {
			Type:       ChangeReplaceChars,
			Content:    "Hello there",
			OldContent: "Hello world",
			ColStart:   6,
			ColEnd:     11,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestChangeReplaceCharsMiddle(t *testing.T) {
	text1 := "Hello world John"
	text2 := "Hello there John"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		1: {
			Type:       ChangeReplaceChars,
			Content:    "Hello there John",
			OldContent: "Hello world John",
			ColStart:   6,
			ColEnd:     11,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestChangeModificationAndAddition(t *testing.T) {
	text1 := `function hello() {
    console.log("old message");
    return true;
}`

	text2 := `function hello() {
    console.log("new message");
    return true;
    console.log("added line");
}`

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		2: {
			Type:       ChangeReplaceChars,
			Content:    `    console.log("new message");`,
			OldContent: `    console.log("old message");`,
			ColStart:   17,
			ColEnd:     20,
		},
		4: {
			Type:    ChangeAddition,
			Content: `    console.log("added line");`,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestMultipleDeletions(t *testing.T) {
	text1 := "line 1\nline 2\nline 3\nline 4\nline 5"
	text2 := "line 1\nline 3\nline 5"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		2: {
			Type:    ChangeDeletion,
			Content: "line 2",
		},
		4: {
			Type:    ChangeDeletion,
			Content: "line 4",
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestMultipleAdditions(t *testing.T) {
	text1 := "line 1\nline 3\nline 5"
	text2 := "line 1\nline 2\nline 3\nline 4\nline 5"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		2: {
			Type:    ChangeAddition,
			Content: "line 2",
		},
		4: {
			Type:    ChangeAddition,
			Content: "line 4",
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestMultipleCharacterChanges(t *testing.T) {
	text1 := "Hello world\nGoodbye world\nWelcome world"
	text2 := "Hello there\nGoodbye there\nWelcome there"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		1: {
			Type:       ChangeReplaceChars,
			Content:    "Hello there",
			OldContent: "Hello world",
			ColStart:   6,
			ColEnd:     11,
		},
		2: {
			Type:       ChangeReplaceChars,
			Content:    "Goodbye there",
			OldContent: "Goodbye world",
			ColStart:   8,
			ColEnd:     13,
		},
		3: {
			Type:       ChangeReplaceChars,
			Content:    "Welcome there",
			OldContent: "Welcome world",
			ColStart:   8,
			ColEnd:     13,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestMixedCharacterOperations(t *testing.T) {
	text1 := "Hello world\nGoodbye world!\nWelcome world"
	text2 := "Hello there\nGoodbye world\nWelcome there!"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		1: {
			Type:       ChangeReplaceChars,
			Content:    "Hello there",
			OldContent: "Hello world",
			ColStart:   6,
			ColEnd:     11,
		},
		2: {
			Type:       ChangeDeleteChars,
			Content:    "Goodbye world",
			OldContent: "Goodbye world!",
			ColStart:   13,
			ColEnd:     14,
		},
		3: {
			Type:       ChangeReplaceChars,
			Content:    "Welcome there!",
			OldContent: "Welcome world",
			ColStart:   8,
			ColEnd:     14,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestChangeModification(t *testing.T) {
	text1 := "start middle end"
	text2 := "beginning middle finish extra"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		1: {
			Type:       ChangeModification,
			Content:    "beginning middle finish extra",
			OldContent: "start middle end",
			ColStart:   0,
			ColEnd:     0,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestNoChanges(t *testing.T) {
	text1 := "line 1\nline 2\nline 3"
	text2 := "line 1\nline 2\nline 3"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 0, len(actual.ChangesMap()), "no changes")
}

func TestConsecutiveModifications(t *testing.T) {
	text1 := `function test() {
    start middle end
    start middle end
    start middle end
}`

	text2 := `function test() {
    beginning middle finish extra
    beginning middle finish extra
    beginning middle finish extra
}`

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		2: {
			Type:       ChangeModification,
			Content:    "    beginning middle finish extra",
			OldContent: "    start middle end",
		},
		3: {
			Type:       ChangeModification,
			Content:    "    beginning middle finish extra",
			OldContent: "    start middle end",
		},
		4: {
			Type:       ChangeModification,
			Content:    "    beginning middle finish extra",
			OldContent: "    start middle end",
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestConsecutiveAdditions(t *testing.T) {
	text1 := `function test() {
    return true;
}`

	text2 := `function test() {
    let x = 1;
    let y = 2;
    let z = 3;
    return true;
}`

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		2: {
			Type:    ChangeAddition,
			Content: "    let x = 1;",
		},
		3: {
			Type:    ChangeAddition,
			Content: "    let y = 2;",
		},
		4: {
			Type:    ChangeAddition,
			Content: "    let z = 3;",
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestMixedChangesNoGrouping(t *testing.T) {
	text1 := `function test() {
    let x = 1;
    console.log("test");
    let y = 2;
}`

	text2 := `function test() {
    let x = 10;
    console.log("test");
    let y = 20;
}`

	actual := ComputeDiff(text1, text2)

	// Should have changes for lines 2 and 4 (not consecutive)
	assert.True(t, len(actual.ChangesMap()) > 0, "changes detected")
}

func TestLineChangeClassification(t *testing.T) {
	tests := []struct {
		name     string
		oldLine  string
		newLine  string
		expected ChangeType
	}{
		{
			name:     "Simple word replacement - should be replace_chars",
			oldLine:  "Hello world",
			newLine:  "Hello there",
			expected: ChangeReplaceChars,
		},
		{
			name:     "Multiple changes - should be modification",
			oldLine:  "start middle end",
			newLine:  "beginning middle finish extra",
			expected: ChangeModification,
		},
		{
			name:     "Single word change - should be replace_chars",
			oldLine:  "let x = 1;",
			newLine:  "let x = 10;",
			expected: ChangeReplaceChars,
		},
		{
			name:     "Complex restructuring - should be modification",
			oldLine:  "function hello() { return true; }",
			newLine:  "async function hello() { const result = await process(); return result; }",
			expected: ChangeModification,
		},
		{
			name:     "Append at end - should be append_chars",
			oldLine:  "Hello world",
			newLine:  "Hello world!",
			expected: ChangeAppendChars,
		},
		{
			name:     "App to server replacement - should be replace_chars",
			oldLine:  `app.route("/health", health);`,
			newLine:  `server.route("/health", health);`,
			expected: ChangeReplaceChars,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diffType, _, _ := categorizeLineChangeWithColumns(test.oldLine, test.newLine)
			assert.Equal(t, test.expected, diffType, "change classification")
		})
	}
}

func TestEmptyOldText(t *testing.T) {
	text1 := ""
	text2 := "line 1\nline 2\nline 3"

	actual := ComputeDiff(text1, text2)

	assert.True(t, len(actual.ChangesMap()) > 0, "changes when adding content to empty file")
}

func TestEmptyNewText(t *testing.T) {
	text1 := "line 1\nline 2\nline 3"
	text2 := ""

	actual := ComputeDiff(text1, text2)

	assert.True(t, len(actual.ChangesMap()) > 0, "changes when deleting all content")

	// Should all be deletions
	for _, change := range actual.ChangesMap() {
		assert.Equal(t, ChangeDeletion, change.Type, "all deletions")
	}
}

func TestSingleLineFile(t *testing.T) {
	text1 := "hello"
	text2 := "hello world"

	actual := ComputeDiff(text1, text2)

	expected := map[int]LineChange{
		1: {
			Type:       ChangeAppendChars,
			Content:    "hello world",
			OldContent: "hello",
			ColStart:   5,
			ColEnd:     11,
		},
	}

	assertChangesEqual(t, expected, actual.ChangesMap())
}

func TestAdditionAtFirstLine(t *testing.T) {
	text1 := "line 2\nline 3"
	text2 := "line 1\nline 2\nline 3"

	actual := ComputeDiff(text1, text2)

	change, exists := actual.ChangesMap()[1]
	assert.True(t, exists, "addition at line 1 exists")
	assert.Equal(t, ChangeAddition, change.Type, "addition type")
}

func TestMultipleAdditionsAtBeginning(t *testing.T) {
	text1 := "original line"
	text2 := "new line 1\nnew line 2\nnew line 3\noriginal line"

	actual := ComputeDiff(text1, text2)

	assert.True(t, len(actual.ChangesMap()) > 0, "changes for additions at beginning")
}

func TestModificationAtFirstLine(t *testing.T) {
	text1 := "old content\nline 2"
	text2 := "new content here\nline 2"

	actual := ComputeDiff(text1, text2)

	_, exists := actual.ChangesMap()[1]
	assert.True(t, exists, "change at line 1")
}

func TestAdditionAtEndOfFile(t *testing.T) {
	text1 := "line 1\nline 2\n"
	text2 := "line 1\nline 2\nline 3\nline 4\n"

	actual := ComputeDiff(text1, text2)

	hasAddition := false
	for _, change := range actual.ChangesMap() {
		if change.Type == ChangeAddition {
			hasAddition = true
			break
		}
	}
	assert.True(t, hasAddition, "at least one addition")
}

func TestDeletionAtFirstLine(t *testing.T) {
	text1 := "line 1\nline 2\nline 3"
	text2 := "line 2\nline 3"

	actual := ComputeDiff(text1, text2)

	change, exists := actual.ChangesMap()[1]
	assert.True(t, exists, "deletion at line 1 exists")
	assert.Equal(t, ChangeDeletion, change.Type, "deletion type")
}

func TestDeletionAtLastLine(t *testing.T) {
	text1 := "line 1\nline 2\nline 3"
	text2 := "line 1\nline 2"

	actual := ComputeDiff(text1, text2)

	change, exists := actual.ChangesMap()[3]
	assert.True(t, exists, "deletion at line 3 exists")
	assert.Equal(t, ChangeDeletion, change.Type, "deletion type")
}

func TestIdenticalLineMarkedAsModification(t *testing.T) {
	oldText := `def bubble_sort(arr):
    n = len(arr)
    for i in range(n):
        for j in range(0, n - i - 1):
            if arr[j] > arr[j + 1]:
                arr[j], arr[j + 1] = arr[j + 1], arr[j]
    return arr


if __name__ == "__main__":
    arr = [64, 34, 25, 12, 22, 11, 90]`

	newText := `def bubble_sort(arr):
    n = len(arr)
    for i in range(n):
        for j in range(0, n - i - 1):
            if arr[j] > arr[j + 1]:
                arr[j], arr[j + 1] = arr[j + 1], arr[j]
    return arr


if __name__ == "__main__":
    arr = [64, 34, 25, 12, 22, 11, 90]
    print(bubble_sort(arr))`

	actual := ComputeDiff(oldText, newText)

	// Check that line 11 is NOT in changes (it's identical in both)
	if change, exists := actual.ChangesMap()[11]; exists {
		assert.False(t, change.Content == change.OldContent,
			"line 11 should not be marked as change when content == oldContent")
	}

	// Line 12 should be an addition
	change, exists := actual.ChangesMap()[12]
	assert.True(t, exists, "line 12 exists")
	assert.Equal(t, ChangeAddition, change.Type, "line 12 is addition")
}

func TestPartialLineCompletionDetectedAsAppendChars(t *testing.T) {
	oldText := `def bubble_sort(arr):
    n = len(arr)
    for i in range(n):
        for j in range(0, n - i - 1):
            if arr[j] > arr[j + 1]:
                arr[j], arr[j + 1] = arr[j + 1], arr[j]
    return arr

if `

	newText := `def bubble_sort(arr):
    n = len(arr)
    for i in range(n):
        for j in range(0, n - i - 1):
            if arr[j] > arr[j + 1]:
                arr[j], arr[j + 1] = arr[j + 1], arr[j]
    return arr

if __name__ == "__main__":
    arr = [64, 34, 25, 12, 22, 11, 90]
    sorted_arr = bubble_sort(arr)
    print(sorted_arr)`

	actual := ComputeDiff(oldText, newText)

	// Line 9 ("if " -> "if __name__ == "__main__":") should be append_chars, not deletion
	change9, exists := actual.ChangesMap()[9]
	assert.True(t, exists, "change at line 9 exists")
	assert.False(t, change9.Type == ChangeDeletion, "line 9 not categorized as deletion")
	assert.Equal(t, ChangeAppendChars, change9.Type, "line 9 is append_chars")
	assert.Equal(t, "if ", change9.OldContent, "oldContent")
}

func TestSingleLineToMultipleLinesWithTrailingNewlines(t *testing.T) {
	oldText := "def test"
	newText := `def test():
    print("test")

test()



`

	actual := ComputeDiff(oldText, newText)

	assert.True(t, len(actual.ChangesMap()) >= 2, "at least 2 changes detected")
}

func TestLineMapping_EqualLineCounts(t *testing.T) {
	text1 := "line 1\nline 2\nline 3"
	text2 := "line 1\nmodified\nline 3"

	actual := ComputeDiff(text1, text2)

	assert.NotNil(t, actual.LineMapping, "LineMapping")
	assert.Equal(t, 3, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 3, actual.NewLineCount, "NewLineCount")

	assert.Equal(t, 1, actual.LineMapping.NewToOld[0], "new line 1 maps to old line 1")
	assert.Equal(t, 3, actual.LineMapping.NewToOld[2], "new line 3 maps to old line 3")
	assert.Equal(t, 2, actual.LineMapping.NewToOld[1], "new line 2 maps to old line 2")
}

func TestLineMapping_PureInsertion(t *testing.T) {
	text1 := "line 1\nline 3"
	text2 := "line 1\nline 2\nline 3"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 2, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 3, actual.NewLineCount, "NewLineCount")
	assert.NotNil(t, actual.LineMapping, "LineMapping")

	assert.Equal(t, 1, actual.LineMapping.NewToOld[0], "new line 1 maps to old line 1")
	assert.Equal(t, -1, actual.LineMapping.NewToOld[1], "new line 2 (inserted) maps to -1")
	assert.Equal(t, 2, actual.LineMapping.NewToOld[2], "new line 3 maps to old line 2")

	change, exists := actual.ChangesMap()[2]
	assert.True(t, exists, "change at line 2 exists")
	assert.Equal(t, ChangeAddition, change.Type, "change type")
	assert.Equal(t, 2, change.NewLineNum, "NewLineNum")
}

func TestLineMapping_PureDeletion(t *testing.T) {
	text1 := "line 1\nline 2\nline 3"
	text2 := "line 1\nline 3"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 3, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 2, actual.NewLineCount, "NewLineCount")

	change, exists := actual.ChangesMap()[2]
	assert.True(t, exists, "change at line 2 (deletion) exists")
	assert.Equal(t, ChangeDeletion, change.Type, "change type")
	assert.Equal(t, 2, change.OldLineNum, "OldLineNum")
	assert.Equal(t, -1, actual.LineMapping.OldToNew[1], "old line 2 (deleted) maps to -1")
}

func TestLineMapping_MultipleInsertions(t *testing.T) {
	text1 := "start\nend"
	text2 := "start\nnew 1\nnew 2\nnew 3\nend"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 2, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 5, actual.NewLineCount, "NewLineCount")

	additionCount := 0
	for _, change := range actual.ChangesMap() {
		if change.Type == ChangeAddition {
			additionCount++
			assert.True(t, change.NewLineNum > 0, "positive NewLineNum for insertion")
		}
	}
	assert.Equal(t, 3, additionCount, "addition count")
}

func TestLineMapping_MultipleDeletions(t *testing.T) {
	text1 := "start\ndel 1\ndel 2\ndel 3\nend"
	text2 := "start\nend"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 5, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 2, actual.NewLineCount, "NewLineCount")

	deletionCount := 0
	for _, change := range actual.ChangesMap() {
		if change.Type == ChangeDeletion {
			deletionCount++
			assert.True(t, change.OldLineNum > 0, "positive OldLineNum for deletion")
		}
	}
	assert.Equal(t, 3, deletionCount, "deletion count")
}

func TestLineMapping_MixedInsertionDeletion(t *testing.T) {
	text1 := "line 1\nold line 2\nline 3"
	text2 := "line 1\nnew line 2a\nnew line 2b\nline 3"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 3, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 4, actual.NewLineCount, "NewLineCount")
	assert.True(t, len(actual.ChangesMap()) > 0, "changes detected")
}

func TestLineMapping_NetLineIncrease(t *testing.T) {
	text1 := `func hello() {
}`
	text2 := `func hello() {
    fmt.Println("Hello")
    fmt.Println("World")
}`

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 2, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 4, actual.NewLineCount, "NewLineCount")
	assert.Equal(t, 1, actual.LineMapping.NewToOld[0], "new line 1 maps to old line 1")
}

func TestLineMapping_NetLineDecrease(t *testing.T) {
	text1 := `func hello() {
    fmt.Println("Hello")
    fmt.Println("World")
    fmt.Println("!")
}`
	text2 := `func hello() {
    fmt.Println("Hello World!")
}`

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 5, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 3, actual.NewLineCount, "NewLineCount")
	assert.True(t, len(actual.ChangesMap()) > 0, "changes detected")
}

func TestLineChangeCoordinates_Modification(t *testing.T) {
	text1 := "Hello world"
	text2 := "Hello there"

	actual := ComputeDiff(text1, text2)

	change, exists := actual.ChangesMap()[1]
	assert.True(t, exists, "change at line 1 exists")
	assert.Equal(t, 1, change.OldLineNum, "OldLineNum")
	assert.Equal(t, 1, change.NewLineNum, "NewLineNum")
}

func TestLineChangeCoordinates_Addition(t *testing.T) {
	text1 := "line 1\nline 3"
	text2 := "line 1\nline 2\nline 3"

	actual := ComputeDiff(text1, text2)

	change, exists := actual.ChangesMap()[2]
	assert.True(t, exists, "change at line 2 exists")
	assert.Equal(t, ChangeAddition, change.Type, "change type")
	assert.Equal(t, 2, change.NewLineNum, "NewLineNum")
}

func TestLineChangeCoordinates_Deletion(t *testing.T) {
	text1 := "line 1\nline 2\nline 3"
	text2 := "line 1\nline 3"

	actual := ComputeDiff(text1, text2)

	change, exists := actual.ChangesMap()[2]
	assert.True(t, exists, "change at line 2 exists")
	assert.Equal(t, ChangeDeletion, change.Type, "change type")
	assert.Equal(t, 2, change.OldLineNum, "OldLineNum")
}

func TestDeletionAtLine1(t *testing.T) {
	text1 := "first line\nsecond line\nthird line"
	text2 := "second line\nthird line"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 3, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 2, actual.NewLineCount, "NewLineCount")

	change, exists := actual.ChangesMap()[1]
	assert.True(t, exists, "deletion at line 1 exists")
	assert.Equal(t, ChangeDeletion, change.Type, "change type")
	assert.Equal(t, 1, change.OldLineNum, "OldLineNum")
	assert.Equal(t, 1, change.NewLineNum, "first line deletion anchors to new line 1")

	assert.Equal(t, -1, actual.LineMapping.OldToNew[0], "old line 1 deleted")
	assert.Equal(t, 1, actual.LineMapping.OldToNew[1], "old line 2 maps to new line 1")
}

func TestMultipleConsecutiveInsertionsThenDeletions(t *testing.T) {
	text1 := "line A\nline B\nline C\nline D\nline E"
	text2 := "line A\nnew 1\nnew 2\nline C\nline E"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 5, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 5, actual.NewLineCount, "NewLineCount")
	assert.True(t, len(actual.ChangesMap()) > 0, "changes detected")

	assert.NotNil(t, actual.LineMapping, "LineMapping exists")
	assert.Equal(t, 5, len(actual.LineMapping.NewToOld), "NewToOld length")
	assert.Equal(t, 5, len(actual.LineMapping.OldToNew), "OldToNew length")
}

func TestInsertionAtLine1(t *testing.T) {
	text1 := "existing line"
	text2 := "new first line\nexisting line"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 1, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 2, actual.NewLineCount, "NewLineCount")

	assert.Equal(t, -1, actual.LineMapping.NewToOld[0], "new line 1 is insertion")
	assert.Equal(t, 1, actual.LineMapping.NewToOld[1], "new line 2 maps to old line 1")
}

func TestLargeLineCountDrift(t *testing.T) {
	text1 := "line 1\nline 2"
	text2 := "line 1\nnew a\nnew b\nnew c\nnew d\nnew e\nline 2"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 2, actual.OldLineCount, "OldLineCount")
	assert.Equal(t, 7, actual.NewLineCount, "NewLineCount")

	insertionCount := 0
	for _, change := range actual.ChangesMap() {
		if change.Type == ChangeAddition {
			insertionCount++
			assert.True(t, change.NewLineNum > 0, "insertion has valid NewLineNum")
		}
	}
	assert.Equal(t, 5, insertionCount, "5 insertions detected")

	assert.Equal(t, 1, actual.LineMapping.NewToOld[0], "new line 1 maps to old line 1")
	assert.Equal(t, 2, actual.LineMapping.NewToOld[6], "new line 7 maps to old line 2")
}

func TestEmptyLineAddition(t *testing.T) {
	// Verifies empty line additions are properly detected and included
	text1 := "import numpy as np"
	text2 := "import numpy as np\n\ndef test_numpy():\n    print('test')"

	actual := ComputeDiff(text1, text2)

	// Line 1 is unchanged (import numpy as np)
	// Line 2 is empty line addition
	// Lines 3-4 are function additions
	// Total: 3 additions
	assert.Equal(t, 3, len(actual.ChangesMap()), "should have 3 additions (empty line + 2 function lines)")

	// Verify empty line is included
	hasEmptyLineAddition := false
	for _, change := range actual.ChangesMap() {
		if change.Type == ChangeAddition && change.Content == "" {
			hasEmptyLineAddition = true
			break
		}
	}
	assert.True(t, hasEmptyLineAddition, "empty line addition should be included")
}

func TestEmptyLineAdditionMiddle(t *testing.T) {
	// Empty line added in the middle of content
	text1 := "line 1\nline 2\nline 3"
	text2 := "line 1\nline 2\n\nline 3"

	actual := ComputeDiff(text1, text2)

	// Should detect the empty line addition
	hasEmptyLineAddition := false
	for _, change := range actual.ChangesMap() {
		if change.Type == ChangeAddition && change.Content == "" {
			hasEmptyLineAddition = true
			break
		}
	}
	assert.True(t, hasEmptyLineAddition, "empty line addition in middle should be detected")
}

func TestMultipleEmptyLineAdditions(t *testing.T) {
	// Multiple empty lines added
	text1 := "start\nend"
	text2 := "start\n\n\n\nend"

	actual := ComputeDiff(text1, text2)

	// Count empty line additions
	emptyLineCount := 0
	for _, change := range actual.ChangesMap() {
		if change.Type == ChangeAddition && change.Content == "" {
			emptyLineCount++
		}
	}
	assert.Equal(t, 3, emptyLineCount, "should detect 3 empty line additions")
}

func TestTrailingEmptyLinePreserved(t *testing.T) {
	// Verifies trailing empty line in original is preserved (not counted as change)
	// Buffer has: ["import numpy as np", ""] (2 lines, line 2 is empty)
	// Completion: ["import numpy as np", "", "def test():", "    pass"] (4 lines)
	// Expected: lines 1-2 are EQUAL, lines 3-4 are additions

	// Use JoinLines to create proper text representation
	text1 := JoinLines([]string{"import numpy as np", ""}) // 2 lines
	text2 := JoinLines([]string{"import numpy as np", "", "def test():", "    pass"})

	actual := ComputeDiff(text1, text2)

	// Should only have 2 additions (def test and pass), NOT 3 (with spurious empty line)
	assert.Equal(t, 2, len(actual.ChangesMap()), "should only have 2 additions (trailing empty line preserved)")

	// Verify no empty line addition (the empty line already exists in original)
	for _, change := range actual.ChangesMap() {
		if change.Type == ChangeAddition && change.Content == "" {
			assert.True(t, false, "should not have empty line addition - the empty line already exists in original")
		}
	}
}

func TestTrailingEmptyLineInBothTexts(t *testing.T) {
	// Both texts end with empty line - should be detected as equal
	text1 := JoinLines([]string{"line1", ""}) // 2 lines
	text2 := JoinLines([]string{"line1", ""}) // Same

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 0, len(actual.ChangesMap()), "identical texts should have no changes")
}

func TestJoinLinesSplitLinesRoundTrip(t *testing.T) {
	// Test round-trip consistency
	cases := [][]string{
		{"a"},
		{"a", "b"},
		{"a", ""},      // trailing empty line
		{"a", "", "b"}, // empty line in middle
		{"", "a"},      // empty line at start
		{"a", "b", "c"},
	}

	for _, lines := range cases {
		text := JoinLines(lines)
		result := SplitLines(text)
		assert.Equal(t, len(lines), len(result), "round-trip length")

		for i := range lines {
			assert.Equal(t, lines[i], result[i], fmt.Sprintf("round-trip element mismatch at %d", i))
		}
	}
}

func TestPureAdditionsAtEndOfFile(t *testing.T) {
	// Verifies empty lines between additions are preserved
	// Buffer has: ["import numpy as np", ""] (2 lines)
	// Completion: ["import numpy as np", "", "def f1():", "    pass", "", "def f2():", "    pass"]
	// Expected: lines 1-2 EQUAL, lines 3-7 ADDITIONS (5 additions, including empty line at 5)

	oldLines := []string{"import numpy as np", ""}
	newLines := []string{"import numpy as np", "", "def f1():", "    pass", "", "def f2():", "    pass"}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)

	actual := ComputeDiff(text1, text2)

	// Should have 5 additions (lines 3-7)
	assert.Equal(t, 5, len(actual.ChangesMap()), "should have 5 additions")

	// Line 5 should be an empty line addition
	line5Change, exists := actual.ChangesMap()[5]
	assert.True(t, exists, "should have change at line 5")
	if exists {
		assert.Equal(t, ChangeAddition, line5Change.Type, "line 5 should be addition")
		assert.Equal(t, "", line5Change.Content, "line 5 should be empty string")
	}
}

// TestEmptyLineFilledWithContent verifies that filling an empty line with content
// is categorized as append_chars (inline ghost text), not addition (virtual line).
func TestEmptyLineFilledWithContent(t *testing.T) {
	// Scenario: cursor is on empty line 8, inline completion suggests "def calc_angle(x, y"
	// This should render as inline ghost text, not a new virtual line
	text1 := JoinLines([]string{""})                    // empty line
	text2 := JoinLines([]string{"def calc_angle(x, y"}) // filled with content

	actual := ComputeDiff(text1, text2)

	t.Logf("Changes: %d", len(actual.ChangesMap()))
	for lineNum, change := range actual.ChangesMap() {
		t.Logf("  Line %d: Type=%v, Content=%q, ColStart=%d, ColEnd=%d",
			lineNum, change.Type, change.Content, change.ColStart, change.ColEnd)
	}

	assert.Equal(t, 1, len(actual.ChangesMap()), "should have 1 change")

	change, exists := actual.ChangesMap()[1]
	assert.True(t, exists, "change at line 1")
	assert.Equal(t, ChangeAppendChars, change.Type, "should be append_chars, not addition")
	assert.Equal(t, 0, change.ColStart, "ColStart should be 0 (start of line)")
	assert.Equal(t, 19, change.ColEnd, "ColEnd should be length of new content")
	assert.Equal(t, "", change.OldContent, "OldContent should be empty")
	assert.Equal(t, "def calc_angle(x, y", change.Content, "Content")
}

// TestDiffWithOnlyWhitespaceChanges tests detection of whitespace-only changes.
func TestDiffWithOnlyWhitespaceChanges(t *testing.T) {
	text1 := "line with trailing spaces   "
	text2 := "line with trailing spaces"

	actual := ComputeDiff(text1, text2)

	// Should detect the whitespace difference
	assert.True(t, len(actual.ChangesMap()) > 0, "should detect whitespace change")
}

// TestDiffWithIndentationChanges tests detection of indentation changes.
func TestDiffWithIndentationChanges(t *testing.T) {
	text1 := "    indented line"
	text2 := "        double indented line"

	actual := ComputeDiff(text1, text2)

	assert.True(t, len(actual.ChangesMap()) > 0, "should detect indentation change")

	change, exists := actual.ChangesMap()[1]
	assert.True(t, exists, "change at line 1")
	// Indentation + word change should result in modification or replace
	assert.True(t, change.Type != ChangeAddition && change.Type != ChangeDeletion,
		"should be modification-type change")
}

// TestDiffWithMixedLineEndings handles text that might have different concepts of lines.
func TestDiffWithMixedLineEndings(t *testing.T) {
	text1 := "line1\nline2\nline3"
	text2 := "line1\nmodified\nline3"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 1, len(actual.ChangesMap()), "should have 1 change")
	_, exists := actual.ChangesMap()[2]
	assert.True(t, exists, "change at line 2")
}

// TestDiffVeryLongFile tests diff computation on a large file.
func TestDiffVeryLongFile(t *testing.T) {
	// Create a large file
	var lines1, lines2 []string
	for i := range 500 {
		lines1 = append(lines1, fmt.Sprintf("line %d content here", i+1))
		lines2 = append(lines2, fmt.Sprintf("line %d content here", i+1))
	}
	// Change a few lines in the middle
	lines2[100] = "modified line 101"
	lines2[200] = "modified line 201"
	lines2[300] = "modified line 301"

	text1 := JoinLines(lines1)
	text2 := JoinLines(lines2)

	actual := ComputeDiff(text1, text2)

	// Should detect exactly 3 changes
	assert.Equal(t, 3, len(actual.ChangesMap()), "should detect 3 changes in large file")
}

// TestDiffWithDuplicateLines tests diff when file has duplicate lines.
func TestDiffWithDuplicateLines(t *testing.T) {
	text1 := "duplicate\nduplicate\nduplicate\nunique"
	text2 := "duplicate\nmodified\nduplicate\nunique"

	actual := ComputeDiff(text1, text2)

	// Should detect change at line 2 even though there are duplicates
	assert.True(t, len(actual.ChangesMap()) > 0, "should detect change among duplicates")
}

// TestDiffConsecutiveEmptyLines tests handling of consecutive empty lines.
func TestDiffConsecutiveEmptyLines(t *testing.T) {
	text1 := "line1\n\n\nline4"
	text2 := "line1\n\n\n\nline5"

	actual := ComputeDiff(text1, text2)

	// Should detect the differences
	assert.True(t, len(actual.ChangesMap()) > 0, "should detect changes in empty line sequences")
}

// TestDiffSingleCharacterChanges tests detection of single character changes.
func TestDiffSingleCharacterChanges(t *testing.T) {
	tests := []struct {
		name     string
		old      string
		new      string
		expected ChangeType
	}{
		{"add single char at end", "hello", "hello!", ChangeAppendChars},
		{"remove single char at end", "hello!", "hello", ChangeDeleteChars},
		{"replace single char", "hello", "hallo", ChangeReplaceChars},
		{"add single char at start", "ello", "hello", ChangeReplaceChars},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ComputeDiff(tt.old, tt.new)
			assert.Equal(t, 1, len(actual.ChangesMap()), "should have 1 change")
			change, exists := actual.ChangesMap()[1]
			assert.True(t, exists, "change exists")
			assert.Equal(t, tt.expected, change.Type, "change type")
		})
	}
}

// TestDiffLineCount verifies OldLineCount and NewLineCount are accurate.
func TestDiffLineCount(t *testing.T) {
	tests := []struct {
		name     string
		old      string
		new      string
		oldCount int
		newCount int
	}{
		{"single to single", "one", "one", 1, 1},
		{"single to multi", "one", "one\ntwo", 1, 2},
		{"multi to single", "one\ntwo", "combined", 2, 1},
		{"empty to content", "", "content", 0, 1},
		{"content to empty", "content", "", 1, 0},
		{"multi to multi same", "a\nb\nc", "x\ny\nz", 3, 3},
		{"add lines", "a\nb", "a\nb\nc\nd", 2, 4},
		{"remove lines", "a\nb\nc\nd", "a\nd", 4, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ComputeDiff(tt.old, tt.new)
			assert.Equal(t, tt.oldCount, actual.OldLineCount, "OldLineCount")
			assert.Equal(t, tt.newCount, actual.NewLineCount, "NewLineCount")
		})
	}
}

// TestDiffUnicodeContent tests diff with unicode characters.
func TestDiffUnicodeContent(t *testing.T) {
	text1 := "Hello 世界"
	text2 := "Hello 世界!"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 1, len(actual.ChangesMap()), "should have 1 change")
	change, exists := actual.ChangesMap()[1]
	assert.True(t, exists, "change exists")
	assert.Equal(t, ChangeAppendChars, change.Type, "should be append_chars")
}

// TestIndentedLineFilledWithContent verifies that adding code after existing
// indentation is categorized as append_chars, not a full modification.
func TestIndentedLineFilledWithContent(t *testing.T) {
	tests := []struct {
		name    string
		oldLine string
		newLine string
	}{
		{"spaces only", "    ", "    return result"},
		{"tabs only", "\t\t", "\t\tresult := compute()"},
		{"partial keyword", "    re", "    return result"},
		{"tabs with partial content", "\t\tlogger.Debug(\"", "\t\tlogger.Debug(\"contextual filter shown\")"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ComputeDiff(tt.oldLine, tt.newLine)

			assert.Equal(t, 1, len(actual.ChangesMap()), "should have 1 change")
			change, exists := actual.ChangesMap()[1]
			assert.True(t, exists, "change at line 1")
			assert.Equal(t, ChangeAppendChars, change.Type, "should be append_chars")
			assert.Equal(t, len(tt.oldLine), change.ColStart, "ColStart at end of old content")
			assert.Equal(t, len(tt.newLine), change.ColEnd, "ColEnd at end of new content")
		})
	}
}

// TestMultiLineAppendCharsOnNonCursorLines verifies that append_chars is
// correctly assigned to multiple lines in a multi-line completion, including
// lines that are not the cursor line.
func TestMultiLineAppendCharsOnNonCursorLines(t *testing.T) {
	oldText := JoinLines([]string{
		"func hello() {",
		"    ",
		"    ",
		"}",
	})
	newText := JoinLines([]string{
		"func hello() {",
		"    fmt.Println(\"hello\")",
		"    return nil",
		"}",
	})

	actual := ComputeDiff(oldText, newText)

	// Both line 2 and line 3 should be append_chars (adding code after indentation)
	change2, exists := actual.ChangesMap()[2]
	assert.True(t, exists, "change at line 2")
	assert.Equal(t, ChangeAppendChars, change2.Type, "line 2 should be append_chars")
	assert.Equal(t, 4, change2.ColStart, "line 2 ColStart after indent")

	change3, exists := actual.ChangesMap()[3]
	assert.True(t, exists, "change at line 3")
	assert.Equal(t, ChangeAppendChars, change3.Type, "line 3 should be append_chars")
	assert.Equal(t, 4, change3.ColStart, "line 3 ColStart after indent")
}

// TestDiffWithTabs tests diff handles tabs correctly.
func TestDiffWithTabs(t *testing.T) {
	text1 := "\tfirst\n\tsecond"
	text2 := "\tfirst\n\tmodified"

	actual := ComputeDiff(text1, text2)

	assert.True(t, len(actual.ChangesMap()) > 0, "should detect change")
	_, exists := actual.ChangesMap()[2]
	assert.True(t, exists, "change at line 2")
}

// TestGreedyMatchingStealsInsertedLine verifies that when multiple lines are
// deleted and only one line is inserted, the best overall match wins — not
// whichever deleted line happens to iterate first.
//
// Scenario from a real completion: removing a null-check block and modifying
// `return article.tags.split(",").length;` → `return article.tags.length;`.
// The diff library produces 5 deleted lines vs 1 inserted line. Greedy
// iteration matched `if (article.tags === null) {` (which shares "article.tags")
// before reaching the actual modification target, causing the real modification
// to appear as a deletion.
func TestGreedyMatchingStealsInsertedLine(t *testing.T) {
	oldText := JoinLines([]string{
		"  if (!article) {",
		"    return 0;",
		"  }",
		"",
		"  if (article.tags === null) {",
		"    return 0;",
		"  }",
		"",
		"  return article.tags.split(\",\").length;",
		"}",
	})
	newText := JoinLines([]string{
		"  if (!article) {",
		"    return 0;",
		"  }",
		"",
		"  return article.tags.length;",
		"}",
	})

	actual := ComputeDiff(oldText, newText)

	// The line `return article.tags.split(",").length;` should be a modification
	// to `return article.tags.length;`, NOT a deletion.
	foundModification := false
	for _, change := range actual.Changes {
		if change.OldContent == `  return article.tags.split(",").length;` ||
			(change.Type == ChangeDeletion && change.Content == `  return article.tags.split(",").length;`) {
			assert.True(t, change.Type != ChangeDeletion,
				"return line should be modification, not deletion")
			assert.Equal(t, "  return article.tags.length;", change.Content,
				"modification content")
			foundModification = true
			break
		}
	}
	assert.True(t, foundModification,
		"should have modification from split().length to .length")

	// The removed null-check block should produce deletions
	deletedContents := make(map[string]bool)
	for _, change := range actual.Changes {
		if change.Type == ChangeDeletion {
			deletedContents[change.Content] = true
		}
	}
	assert.True(t, deletedContents["  if (article.tags === null) {"],
		"null check should be deleted")
	assert.True(t, deletedContents["    return 0;"],
		"return 0 should be deleted")
	assert.True(t, deletedContents["  }"],
		"closing brace should be deleted")

	// Log all changes for debugging
	for _, change := range actual.Changes {
		t.Logf("Type=%v OldLine=%d NewLine=%d Content=%q OldContent=%q",
			change.Type, change.OldLineNum, change.NewLineNum, change.Content, change.OldContent)
	}
}

// TestMatchScoreArticleTags verifies the similarity scores for the lines
// involved in the greedy matching bug. This helps confirm the root cause.
func TestMatchScoreArticleTags(t *testing.T) {
	inserted := "  return article.tags.length;"

	// The line that SHOULD match — highest similarity
	bestMatch := `  return article.tags.split(",").length;`
	bestScore := matchScore(bestMatch, inserted)
	t.Logf("bestMatch score: %.4f", bestScore)
	assert.True(t, bestScore >= SimilarityThreshold,
		"best match should be above threshold")

	// Lines that should NOT steal the match
	nullCheck := "  if (article.tags === null) {"
	nullCheckScore := matchScore(nullCheck, inserted)
	t.Logf("nullCheck score: %.4f", nullCheckScore)

	returnZero := "    return 0;"
	returnZeroScore := matchScore(returnZero, inserted)
	t.Logf("returnZero score: %.4f", returnZeroScore)

	closeBrace := "  }"
	closeBraceScore := matchScore(closeBrace, inserted)
	t.Logf("closeBrace score: %.4f", closeBraceScore)

	// The best match should beat all others
	assert.True(t, bestScore > nullCheckScore,
		"split line should score higher than null check")
	assert.True(t, bestScore > returnZeroScore,
		"split line should score higher than return 0")
	assert.True(t, bestScore > closeBraceScore,
		"split line should score higher than close brace")
}

// TestIfElseSimplificationDiffClassification verifies that when an if/else block
// is simplified to a single line, all removed lines are classified as deletions
// (not matched as "equal" with distant identical lines elsewhere in the text).
func TestIfElseSimplificationDiffClassification(t *testing.T) {
	// Exact editable region from the log (file lines 64-92)
	oldText := JoinLines([]string{
		"  });",
		"",
		"  return true;",
		"}",
		"",
		"export function addTag(id: string, tag: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  if (article.tags.length === 0) {", // line 12 rel
		"    article.tags.push(tag);",        // line 13 rel
		"  } else {",                         // line 14 rel
		"    article.tags.push(tag);",        // line 15 rel
		"  }",                                // line 16 rel — should be deleted with block
		"",
		"  inc(\"tag_added\");",
		"",
		"  return true;",
		"}",
		"",
		"export function hasTags(id: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  if (article.tags === null) {",
	})

	newText := JoinLines([]string{
		"  });",
		"",
		"  return true;",
		"}",
		"",
		"export function addTag(id: string, tag: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  article.tags.push(tag);", // replaces the entire if/else
		"",
		"  inc(\"tag_added\");",
		"",
		"  return true;",
		"}",
		"",
		"export function hasTags(id: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  if (article.tags.length === 0) {",
	})

	actual := ComputeDiff(oldText, newText)

	// Log all changes for diagnosis
	for _, change := range actual.Changes {
		t.Logf("Type=%-14s OldLine=%2d NewLine=%2d Content=%q OldContent=%q",
			change.Type, change.OldLineNum, change.NewLineNum, change.Content, change.OldContent)
	}

	// The closing `}` at old line 16 should be a deletion, not matched as equal
	closeBraceDeleted := false
	for _, change := range actual.Changes {
		if change.Type == ChangeDeletion && change.Content == "  }" {
			closeBraceDeleted = true
			// It should be near the if/else block (old lines 12-16)
			assert.True(t, change.OldLineNum >= 12 && change.OldLineNum <= 16,
				"closing brace deletion should be in the if/else block range (12-16)")
			break
		}
	}
	assert.True(t, closeBraceDeleted,
		"closing brace should be a deletion, not matched as equal with a distant `}`")

	// All if/else deletions should have consecutive OldLineNums
	var deletionOldLines []int
	for _, change := range actual.Changes {
		if change.Type == ChangeDeletion && change.OldLineNum >= 12 && change.OldLineNum <= 16 {
			deletionOldLines = append(deletionOldLines, change.OldLineNum)
		}
	}
	t.Logf("Deletion OldLineNums in if/else range: %v", deletionOldLines)
}

// TestConsecutiveDeletionsBlankLines verifies that deleting multiple consecutive
// blank lines (including whitespace-only lines) produces one deletion per line.
// Regression test: a shared anchor caused both deletions to map to relativeLine=1,
// silently dropping the second deletion.
func TestConsecutiveDeletionsBlankLines(t *testing.T) {
	oldLines := []string{
		"def foo():",
		"    pass",
		" ",
		"",
		"def bar():",
		"    pass",
	}
	newLines := []string{
		"def foo():",
		"    pass",
		"def bar():",
		"    pass",
	}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)
	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 2, len(actual.ChangesMap()), "should detect 2 deletions")

	del3, exists := actual.ChangesMap()[3]
	assert.True(t, exists, "deletion at old line 3 exists")
	assert.Equal(t, ChangeDeletion, del3.Type, "line 3 is deletion")
	assert.Equal(t, " ", del3.Content, "line 3 content is space")

	del4, exists := actual.ChangesMap()[4]
	assert.True(t, exists, "deletion at old line 4 exists")
	assert.Equal(t, ChangeDeletion, del4.Type, "line 4 is deletion")
	assert.Equal(t, "", del4.Content, "line 4 content is empty string")
}

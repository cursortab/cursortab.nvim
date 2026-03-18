// Package contextfilter implements a contextual filter that predicts the
// probability a user will accept a code completion based on cursor context.
//
// The model is a logistic regression classifier extracted from GitHub Copilot's
// extension (modules 77744 and 38965). It scores 8 numeric features and 3
// one-hot categorical features (language, last character, last non-whitespace
// character) to produce a probability in [0, 1].
//
// Sources:
//   - Copilot extension constants (module 77744) and scoring function (module 38965):
//     https://thakkarparth007.github.io/copilot-explorer/posts/copilot-internals.html
//   - de Moor, van Deursen, Izadi. "A Transformer-Based Approach for Smart
//     Invocation of Automatic Code Completion" (2024): https://arxiv.org/abs/2405.14753
//   - Mozannar et al. "When to Show a Suggestion?" (AAAI 2024):
//     https://arxiv.org/abs/2306.04930
package gating

import (
	"math"
	"strings"
	"time"
)

// Threshold is the minimum score (0-1) for showing a completion.
// Requests scoring below this are suppressed without contacting the provider.
const Threshold = 0.35

const numWeights = 221

// intercept is the logistic regression bias term.
const intercept = -0.3043572714994554

// weights contains all 221 logistic regression weights extracted from the
// Copilot extension (module 77744). Layout:
//
//	[0]       previousLabel
//	[1]       afterCursorWhitespace (0/1)
//	[2]       log(1 + secondsSinceLastDecision)
//	[3]       log(1 + prefixLength)
//	[4]       log(1 + trimmedPrefixLength)
//	[5]       log(1 + documentLength)
//	[6]       log(1 + cursorByteOffset)
//	[7]       (cursorByteOffset + 0.5) / (1 + documentLength)
//	[8..28]   one-hot language (21 slots: 0=unknown + 20 languages)
//	[29..124] one-hot last char of prefix (96 slots: 0=none + 95 printable ASCII)
//	[125..220] one-hot last char of trimmed prefix (96 slots: 0=none + 95 printable ASCII)
var weights = [221]float64{
	0.9978708359643611,    // [0] previousLabel
	0.7001905605239328,    // [1] afterCursorWhitespace
	-0.1736749244124868,   // [2] timeSinceLastDecision
	-0.22994157947320112,  // [3] prefixLength
	0.13406692641682572,   // [4] trimmedPrefixLength
	-0.007751370662011853, // [5] documentLength
	0.0057783222035240715, // [6] cursorByteOffset
	0.41910878254476003,   // [7] relativePosition
	// Language weights [8..28] (index 0 = unknown language)
	-0.1621657125711092,  // [8]  unknown
	0.13770814958908187,  // [9]  javascript
	-0.06036011308184006, // [10] typescript
	-0.07351180985800129, // [11] typescriptreact
	0,                    // [12] python
	-0.05584878151248109, // [13] vue
	0.30618794079412015,  // [14] php
	-0.1282197982598485,  // [15] dart
	0.10951859303997555,  // [16] javascriptreact
	0.1700461782788777,   // [17] go
	-0.3346057842644757,  // [18] css
	0.22497985923128136,  // [19] cpp
	0,                    // [20] html
	-0.44038101825774356, // [21] scss
	-0.6540115939236782,  // [22] markdown
	0.16595600081341702,  // [23] csharp
	0.20733910722385135,  // [24] java
	-0.1337033766105696,  // [25] json
	-0.06923072125290894, // [26] rust
	-0.05806684191976292, // [27] ruby
	0.3583334671633344,   // [28] c
	// Last char weights [29..124] (index 0 = no char / non-ASCII)
	-0.47357732824944315,  // [29]  (none)
	0.17810871365594377,   // [30]  ' '
	0.42268219963946685,   // [31]  '!'
	0,                     // [32]  '"'
	0,                     // [33]  '#'
	-0.16379620467004602,  // [34]  '$'
	-0.43893868831061167,  // [35]  '%'
	0,                     // [36]  '&'
	0.11570094006709251,   // [37]  '\''
	0.9326431262654882,    // [38]  '('
	-0.9990110509203912,   // [39]  ')'
	-0.44125275652726503,  // [40]  '*'
	-0.15840786997162004,  // [41]  '+'
	-0.4600396256644451,   // [42]  ','
	-0.018814811994044403, // [43] '-'
	0.09230944537175266,   // [44]  '.'
	0.025814790934742798,  // [45]  '/'
	-1.0940162204190154,   // [46]  '0'
	-0.9407503631235489,   // [47]  '1'
	-0.9854303778694269,   // [48]  '2'
	-1.1045822488262245,   // [49]  '3'
	-1.1417299456573262,   // [50]  '4'
	-1.5623704405345513,   // [51]  '5'
	-0.4157473855795939,   // [52]  '6'
	-1.0244257735561713,   // [53]  '7'
	-0.7477401944601753,   // [54]  '8'
	-1.1275109699068402,   // [55]  '9'
	-0.0714715633552533,   // [56]  ':'
	-1.1408628006786907,   // [57]  ';'
	-1.0409898655074672,   // [58]  '<'
	-0.2288889836518878,   // [59]  '='
	-0.5469549893760344,   // [60]  '>'
	-0.181946611106845,    // [61]  '?'
	0.1264329316374918,    // [62]  '@'
	0,                     // [63]  'A'
	0,                     // [64]  'B'
	0.312206968554707,     // [65]  'C'
	-0.3656436392517924,   // [66]  'D'
	0.23655650686038968,   // [67]  'E'
	0.1014912419901576,    // [68]  'F'
	0,                     // [69]  'G'
	0.06287549221765308,   // [70]  'H'
	0,                     // [71]  'I'
	0,                     // [72]  'J'
	0.19027065218932154,   // [73]  'K'
	-0.8519502045974378,   // [74]  'L'
	0,                     // [75]  'M'
	0.23753599905971923,   // [76]  'N'
	0.2488809322489166,    // [77]  'O'
	0.019969251907983224,  // [78]  'P'
	0,                     // [79]  'Q'
	0.06916505526229488,   // [80]  'R'
	0.29053356359188204,   // [81]  'S'
	-0.14484456555431657,  // [82]  'T'
	0.014768129429370188,  // [83]  'U'
	-0.15051464926341374,  // [84]  'V'
	0.07614835502776021,   // [85]  'W'
	-0.3317489901313935,   // [86]  'X'
	0,                     // [87]  'Y'
	0,                     // [88]  'Z'
	0.04921938684669103,   // [89]  '['
	-0.28248576768353445,  // [90]  '\\'
	-0.9708816204525345,   // [91]  ']'
	-1.3560464522265527,   // [92]  '^'
	0.014165375212383239,  // [93]  '_'
	-0.23924166472544983,  // [94]  '`'
	0.10006595730248855,   // [95]  'a'
	0.09867233147279562,   // [96]  'b'
	0.32330430333220644,   // [97]  'c'
	-0.058625706114180595, // [98] 'd'
	0.17149853105783947,   // [99]  'e'
	0.4436484054395367,    // [100] 'f'
	0.047189049576707255,  // [101] 'g'
	0.16832520944790552,   // [102] 'h'
	0.1117259900942179,    // [103] 'i'
	-0.35469010329927253,  // [104] 'j'
	0,                     // [105] 'k'
	-0.1528189124465582,   // [106] 'l'
	-0.3804848349564939,   // [107] 'm'
	0.07278077320753953,   // [108] 'n'
	0.13263786480064088,   // [109] 'o'
	0.22920682659292527,   // [110] 'p'
	1.1512955314336537,    // [111] 'q'
	0,                     // [112] 'r'
	0.016939862282340023,  // [113] 's'
	0.4242994650403408,    // [114] 't'
	0.12759835577444986,   // [115] 'u'
	-0.5577261135825583,   // [116] 'v'
	-0.19764560943067672,  // [117] 'w'
	-0.4042102444736004,   // [118] 'x'
	0.12063461617733708,   // [119] 'y'
	-0.2933966817484834,   // [120] 'z'
	0.2715683893968593,    // [121] '{'
	0,                     // [122] '|'
	-0.7138548251238751,   // [123] '}'
	0,                     // [124] '~'
	// Last non-WS char weights [125..220] (index 0 = no char / non-ASCII)
	-0.023066228703035277, // [125] (none)
	0,                     // [126] ' '
	-0.06383043976746139,  // [127] '!'
	0.09683723720709651,   // [128] '"'
	-0.7337151424080791,   // [129] '#'
	0,                     // [130] '$'
	-0.27191370124625525,  // [131] '%'
	0.2819781269656171,    // [132] '&'
	-0.08711496549050252,  // [133] '\''
	0.11048604909969338,   // [134] '('
	-0.0934849550450534,   // [135] ')'
	0.0721001250772912,    // [136] '*'
	0.2589126797890794,    // [137] '+'
	0.6729582659532254,    // [138] ','
	-0.21921032738244908,  // [139] '-'
	-0.21535277468651456,  // [140] '.'
	-0.45474006124091354,  // [141] '/'
	-0.05861820126419139,  // [142] '0'
	-0.007875306207720204, // [143] '1'
	-0.056661261678809284, // [144] '2'
	0.17727881404222662,   // [145] '3'
	0.23603713348534658,   // [146] '4'
	0.17485861412377932,   // [147] '5'
	-0.5737483768696752,   // [148] '6'
	-0.38220029570342745,  // [149] '7'
	-0.5202722985519168,   // [150] '8'
	-0.37187947527657256,  // [151] '9'
	0.47155277792990113,   // [152] ':'
	-0.12077912346691123,  // [153] ';'
	0.47825628981545326,   // [154] '<'
	0.4736704404000214,    // [155] '='
	-0.1615218651546898,   // [156] '>'
	0.18362447973513005,   // [157] '?'
	0,                     // [158] '@'
	0,                     // [159] 'A'
	-0.18183417425866824,  // [160] 'B'
	0,                     // [161] 'C'
	0,                     // [162] 'D'
	-0.2538532305733833,   // [163] 'E'
	-0.1303692690676528,   // [164] 'F'
	-0.4073577969188216,   // [165] 'G'
	0.04172985870928789,   // [166] 'H'
	-0.1704527388573901,   // [167] 'I'
	0,                     // [168] 'J'
	0,                     // [169] 'K'
	0.7536858953385828,    // [170] 'L'
	-0.44703159588787644,  // [171] 'M'
	0,                     // [172] 'N'
	-0.7246484085580873,   // [173] 'O'
	-0.21378128540782063,  // [174] 'P'
	0,                     // [175] 'Q'
	0.037461090552656146,  // [176] 'R'
	-0.16205852364367032,  // [177] 'S'
	-0.10973952064404884,  // [178] 'T'
	0.017468043407647377,  // [179] 'U'
	-0.1288980387397392,   // [180] 'V'
	0,                     // [181] 'W'
	0,                     // [182] 'X'
	0,                     // [183] 'Y'
	-1.218692715379445,    // [184] 'Z'
	0.05536949662193305,   // [185] '['
	-0.3763799844799116,   // [186] '\\'
	-0.1845001725624579,   // [187] ']'
	-0.1615576298149558,   // [188] '^'
	0,                     // [189] '_'
	-0.15373262203249874,  // [190] '`'
	-0.04603412604270418,  // [191] 'a'
	0,                     // [192] 'b'
	-0.3068149681460828,   // [193] 'c'
	0.09412352468269412,   // [194] 'd'
	0,                     // [195] 'e'
	0.09116543650609721,   // [196] 'f'
	0.06065865264082559,   // [197] 'g'
	0.05688267379386188,   // [198] 'h'
	-0.05873945477722306,  // [199] 'i'
	0,                     // [200] 'j'
	0.14532465133322153,   // [201] 'k'
	0.1870857769705463,    // [202] 'l'
	0.36304258043185555,   // [203] 'm'
	0.1411392422180405,    // [204] 'n'
	0.0630388629716367,    // [205] 'o'
	0,                     // [206] 'p'
	-1.1170522012450395,   // [207] 'q'
	0.16133697772771127,   // [208] 'r'
	0.15908534390781448,   // [209] 's'
	-0.23485453704002232,  // [210] 't'
	-0.1419980841417892,   // [211] 'u'
	0.21909510179526218,   // [212] 'v'
	0.39948420260153766,   // [213] 'w'
	0.40802294284289187,   // [214] 'x'
	0.15403767653746853,   // [215] 'y'
	0,                     // [216] 'z'
	0.19764784115096676,   // [217] '{'
	0.584914157527457,     // [218] '|'
	-0.4573883817015294,   // [219] '}'
	0,                     // [220] '~'
}

// languageIndex maps language identifiers to one-hot indices.
var languageIndex = map[string]int{
	"javascript":      1,
	"typescript":      2,
	"typescriptreact": 3,
	"python":          4,
	"vue":             5,
	"php":             6,
	"dart":            7,
	"javascriptreact": 8,
	"go":              9,
	"css":             10,
	"cpp":             11,
	"html":            12,
	"scss":            13,
	"markdown":        14,
	"csharp":          15,
	"java":            16,
	"json":            17,
	"rust":            18,
	"ruby":            19,
	"c":               20,
}

// extToLanguage maps file extensions to Copilot language identifiers.
var extToLanguage = map[string]string{
	".js":   "javascript",
	".mjs":  "javascript",
	".cjs":  "javascript",
	".jsx":  "javascriptreact",
	".ts":   "typescript",
	".tsx":  "typescriptreact",
	".py":   "python",
	".vue":  "vue",
	".php":  "php",
	".dart": "dart",
	".go":   "go",
	".css":  "css",
	".cpp":  "cpp",
	".cc":   "cpp",
	".cxx":  "cpp",
	".h":    "cpp",
	".hpp":  "cpp",
	".html": "html",
	".htm":  "html",
	".scss": "scss",
	".md":   "markdown",
	".cs":   "csharp",
	".java": "java",
	".json": "json",
	".rs":   "rust",
	".rb":   "ruby",
	".c":    "c",
}

// charIndex returns the one-hot index for a printable ASCII character.
// Returns 0 for non-printable or non-ASCII characters.
func charIndex(ch byte) int {
	if ch >= 32 && ch <= 126 {
		return int(ch) - 31
	}
	return 0
}

// Input contains the cursor context needed to compute a filter score.
type Input struct {
	Lines         []string  // Document lines
	Row           int       // 1-based cursor row
	Col           int       // 0-based cursor column (byte offset within line)
	FileExtension string    // Lowercase file extension including dot (e.g. ".go")
	PreviousLabel bool      // Whether the previous filter call resulted in "show"
	LastDecision  time.Time // When the last accept/reject/suppress happened
	Now           time.Time // Current time
}

// Features builds the 221-element feature vector from an Input.
func Features(in Input) [numWeights]float64 {
	var f [numWeights]float64

	if in.PreviousLabel {
		f[0] = 1
	}
	if AfterCursorIsWhitespace(in.Lines, in.Row, in.Col) {
		f[1] = 1
	}
	if !in.LastDecision.IsZero() {
		f[2] = math.Log(1 + in.Now.Sub(in.LastDecision).Seconds())
	}

	line, col := CurrentLine(in.Lines, in.Row, in.Col)
	prefix := line[:col]
	f[3] = math.Log(1 + float64(len(prefix)))
	f[4] = math.Log(1 + float64(len(strings.TrimRight(prefix, " \t"))))

	docLen := DocumentByteLength(in.Lines)
	cursorOff := ByteOffset(in.Lines, in.Row, in.Col)
	f[5] = math.Log(1 + float64(docLen))
	f[6] = math.Log(1 + float64(cursorOff))
	f[7] = (float64(cursorOff) + 0.5) / (1.0 + float64(docLen))

	lang := extToLanguage[in.FileExtension]
	if idx, ok := languageIndex[lang]; ok {
		f[8+idx] = 1
	} else {
		f[8] = 1
	}

	if col > 0 {
		f[29+charIndex(line[col-1])] = 1
	} else {
		f[29] = 1
	}

	if nwc, ok := LastNonWSChar(line, col); ok {
		f[125+charIndex(nwc)] = 1
	} else {
		f[125] = 1
	}

	return f
}

// Score computes the probability that the user will accept a completion
// in the given context. Returns a value in [0, 1].
func Score(in Input) float64 {
	f := Features(in)
	s := intercept
	for i, v := range f {
		s += weights[i] * v
	}
	return sigmoid(s)
}

// ShouldSuppress returns true if the score is below the acceptance threshold.
func ShouldSuppress(score float64) bool {
	return score < Threshold
}

func AfterCursorIsWhitespace(lines []string, row, col int) bool {
	if row < 1 || row > len(lines) {
		return true
	}
	line := lines[row-1]
	if col >= len(line) {
		return true
	}
	return strings.TrimSpace(line[col:]) == ""
}

func CurrentLine(lines []string, row, col int) (string, int) {
	if row < 1 || row > len(lines) {
		return "", 0
	}
	line := lines[row-1]
	if col > len(line) {
		col = len(line)
	}
	return line, col
}

func DocumentByteLength(lines []string) int {
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	return total
}

func ByteOffset(lines []string, row, col int) int {
	offset := 0
	for i := 0; i < row-1 && i < len(lines); i++ {
		offset += len(lines[i]) + 1
	}
	if row >= 1 && row <= len(lines) {
		c := min(col, len(lines[row-1]))
		offset += c
	}
	return offset
}

func LastNonWSChar(line string, col int) (byte, bool) {
	for i := col - 1; i >= 0; i-- {
		if line[i] != ' ' && line[i] != '\t' {
			return line[i], true
		}
	}
	return 0, false
}

func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

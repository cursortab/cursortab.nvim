package buffer

// Batch represents deferred editor operations
type Batch interface {
	Execute() error
}

// Position is the cursor and the size of the current buffer when the editor
// sent an event.
type Position struct {
	Row    int // 1-indexed
	Col    int // 0-indexed byte column
	Offset int // Byte offset of the cursor, one newline per line counted
	Size   int // Bytes of the buffer, one newline per line counted
}

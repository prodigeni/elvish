package edit

import (
	"os"
	"fmt"
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"
	"./tty"
	"../util"
)

// cell is an indivisible unit on the screen. It is not necessarily 1 column
// wide.
type cell struct {
	rune
	width byte
	attr string
}

// pos is the position within a buffer.
type pos struct {
	line, col int
}

// buffer reflects a continuous range of lines on the terminal. The Unix
// terminal API provides only awkward ways of querying the terminal buffer, so
// we keep an internal reflection and do one-way synchronizations (buffer ->
// terminal, and not the other way around). This requires us to exactly match
// the terminal's idea of the width of characters (wcwidth) and where to
// insert soft carriage returns, so there could be bugs.
type buffer struct {
	width, col, indent int
	cells [][]cell // cells reflect len(cells) lines on the terminal.
	dot pos // dot is what the user perceives as the cursor.
}

func newBuffer(width int) *buffer {
	return &buffer{width: width, cells: [][]cell{make([]cell, 0, width)}}
}

func (b *buffer) appendCell(c cell) {
	n := len(b.cells)
	b.cells[n-1] = append(b.cells[n-1], c)
	b.col += int(c.width)
}

func (b *buffer) appendLine() {
	b.cells = append(b.cells, make([]cell, 0, b.width))
	b.col = 0
}

func (b *buffer) newline() {
	b.appendLine()

	if b.indent > 0 {
		for i := 0; i < b.indent; i++ {
			b.appendCell(cell{rune: ' ', width: 1})
		}
	}
}

func (b *buffer) extend(b2 *buffer) {
	if b2 != nil && b2.cells != nil {
		b.cells = append(b.cells, b2.cells...)
		b.col = b2.col
	}
}

// write appends a single rune to a buffer.
func (b *buffer) write(r rune, attr string) {
	if r == '\n' {
		b.newline()
		return
	} else if !unicode.IsPrint(r) {
		// XXX unprintable runes are dropped silently
		return
	}
	wd := wcwidth(r)
	c := cell{r, byte(wd), attr}

	if b.col + wd > b.width {
		b.newline()
		b.appendCell(c)
	} else if b.col + wd == b.width {
		b.appendCell(c)
		b.newline()
	} else {
		b.appendCell(c)
	}
}

func (b *buffer) writes(s string, attr string) {
	for _, r := range s {
		b.write(r, attr)
	}
}

func (b *buffer) writePadding(w int, attr string) {
	b.writes(strings.Repeat(" ", w), attr)
}

func (b *buffer) line() int {
	return len(b.cells) - 1
}

func (b *buffer) cursor() pos {
	return pos{len(b.cells) - 1, b.col}
}

// writer is the part of an Editor responsible for keeping the status of and
// updating the screen.
type writer struct {
	file *os.File
	oldBuf *buffer
}

func newWriter(f *os.File) *writer {
	writer := &writer{file: f, oldBuf: newBuffer(0)}
	return writer
}

// deltaPos calculates the escape sequence needed to move the cursor from one
// position to another.
func deltaPos(from, to pos) []byte {
	buf := new(bytes.Buffer)
	if from.line < to.line {
		// move down
		buf.WriteString(fmt.Sprintf("\033[%dB", to.line - from.line))
	} else if from.line > to.line {
		// move up
		buf.WriteString(fmt.Sprintf("\033[%dA", from.line - to.line))
	}
	if from.col < to.col {
		// move right
		buf.WriteString(fmt.Sprintf("\033[%dC", to.col - from.col))
	} else if from.col > to.col {
		// move left
		buf.WriteString(fmt.Sprintf("\033[%dD", from.col - to.col))
	}
	return buf.Bytes()
}

// commitBuffer updates the terminal display to reflect current buffer.
// TODO Instead of erasing w.oldBuf entirely and then draw buf, compute a
// delta between w.oldBuf and buf
func (w *writer) commitBuffer(buf *buffer) error {
	bytesBuf := new(bytes.Buffer)

	pLine := w.oldBuf.dot.line
	if pLine > 0 {
		fmt.Fprintf(bytesBuf, "\033[%dA", pLine)
	}
	bytesBuf.WriteString("\r\033[J")

	attr := ""
	for i, line := range buf.cells {
		if i > 0 {
			bytesBuf.WriteString("\n")
		}
		for _, c := range line {
			if c.width > 0 && c.attr != attr {
				fmt.Fprintf(bytesBuf, "\033[m\033[%sm", c.attr)
				attr = c.attr
			}
			bytesBuf.WriteString(string(c.rune))
		}
	}
	if attr != "" {
		bytesBuf.WriteString("\033[m")
	}
	bytesBuf.Write(deltaPos(buf.cursor(), buf.dot))

	_, err := w.file.Write(bytesBuf.Bytes())
	if err != nil {
		return err
	}

	w.oldBuf = buf
	return nil
}

// refresh redraws the line editor. The dot is passed as an index into text;
// the corresponding position will be calculated.
func (w *writer) refresh(bs *editorState) error {
	fd := int(w.file.Fd())
	width := int(tty.GetWinsize(fd).Col)

	var bufLine, bufMode, bufTips, bufCompletion, buf *buffer
	// bufLine
	b := newBuffer(width)
	bufLine = b

	b.writes(bs.prompt, attrForPrompt)

	if b.col * 2 < b.width {
		b.indent = b.col
	}

	// i keeps track of number of bytes written.
	i := 0
	if bs.dot == 0 {
		b.dot = b.cursor()
	}

	comp := bs.completion
	var suppress = false
	for _, token := range bs.tokens {
		for _, r := range token.Val {
			if suppress && i < comp.end {
				// Silence the part that is being completed
			} else {
				b.write(r, attrForType[token.Typ])
			}
			i += utf8.RuneLen(r)
			if comp != nil && comp.current != -1 && i == comp.start {
				// Put the current candidate and instruct text up to comp.end
				// to be suppressed. The cursor should be placed correctly
				// (i.e. right after the candidate)
				for _, part := range comp.candidates[comp.current].parts {
					attr := attrForType[comp.typ]
					if part.completed {
						attr += attrForCompleted
					}
					b.writes(part.text, attr)
				}
				suppress = true
			}
			if bs.dot == i {
				b.dot = b.cursor()
			}
		}
	}

	// Write rprompt
	padding := b.width - 1 - b.col - wcwidths(bs.rprompt)
	if padding >= 1 {
		b.writePadding(padding, "")
		b.writes(bs.rprompt, attrForRprompt)
	}

	// bufMode
	if bs.mode != ModeInsert {
		b := newBuffer(width)
		bufMode = b
		switch bs.mode {
		case ModeCommand:
			b.writes("-- COMMAND --", attrForMode)
		case ModeCompleting:
			b.writes("-- COMPLETING --", attrForMode)
		}
	}

	// bufTips
	if len(bs.tips) > 0 {
		b := newBuffer(width)
		bufTips = b
		b.writes(strings.Join(bs.tips, ", "), attrForTip)
	}

	// bufCompletion
	if comp != nil {
		b := newBuffer(width)
		bufCompletion = b
		// Layout candidates in multiple columns
		cands := comp.candidates

		// First decide the shape (# of rows and columns)
		colWidth := 0
		colMargin := 2
		for _, cand := range cands {
			width := wcwidths(cand.text)
			if colWidth < width {
				colWidth = width
			}
		}

		cols := (b.width + colMargin) / (colWidth + colMargin)
		if cols == 0 {
			cols = 1
		}
		lines := util.CeilDiv(len(cands), cols)

		for i := 0; i < lines; i++ {
			if i > 0 {
				b.newline()
			}
			for j := 0; j < cols; j++ {
				k := j * lines + i
				if k >= len(cands) {
					continue
				}
				var attr string
				if k == comp.current {
					attr = attrForCurrentCompletion
				}
				text := cands[k].text
				b.writes(text, attr)
				b.writePadding(colWidth - wcwidths(text), attr)
				b.writePadding(colMargin, "")
			}
		}
	}

	// reuse bufLine
	buf = bufLine
	buf.extend(bufMode)
	buf.extend(bufTips)
	buf.extend(bufCompletion)
	return w.commitBuffer(buf)
}

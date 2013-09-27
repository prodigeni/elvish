package edit

import (
	"time"
	"bufio"
	"errors"
	"../async"
)

var EscTimeout = time.Millisecond * 10

// reader is the part of an Editor responsible for reading and decoding
// terminal key sequences.
type reader struct {
	timed *async.TimedReader
	buffed *bufio.Reader
}

func newReader(tr *async.TimedReader) *reader {
	return &reader{
		tr,
		bufio.NewReaderSize(tr, 0),
	}
}

// type readerState func(rune) (bool, readerState)

// G3 style function key sequences: ^[O followed by exactly one character.
var g3Seq = map[rune]rune{
	// F1-F4: xterm, libvte and tmux
	'P': F1, 'Q': F2,
	'R': F3, 'S': F4,

	// Home and End: libvte
	'H': Home, 'F': End,
}

var BadEscSeq = errors.New("bad function key sequence")

func (rd *reader) readKey() (k Key, err error) {
	r, _, err := rd.buffed.ReadRune()

	if err != nil {
		return
	}

	switch r {
	case 0x0:
		k = Key{'`', Ctrl} // ^@
	case 0x1d:
		k = Key{'6', Ctrl} // ^^
	case 0x1f:
		k = Key{'/', Ctrl} // ^_
	case 0x7f: // ^? Backspace
		k = Key{Backspace, 0}
	case 0x1b: // ^[ Escape
		rd.timed.Timeout = EscTimeout
		defer func() { rd.timed.Timeout = -1 }()
		r2, _, e := rd.buffed.ReadRune()
		if e == async.Timeout {
			return Key{'[', Ctrl}, nil
		} else if e != nil {
			return ZeroKey, e
		}
		switch r2 {
		case '[':
			// CSI style function key sequence, looks like [\d;]*[^\d;]
			// Read numeric parameters (if any)
			nums := make([]int, 0, 2)
			for {
				var e error
				r, _, e = rd.buffed.ReadRune()
				// Timeout can only happen at first ReadRune.
				if e == async.Timeout {
					return Key{'[', Alt}, nil
				} else if e != nil {
					return ZeroKey, e
				}
				// After first rune read we turn off the timeout
				rd.timed.Timeout = -1
				if r != ';' && (r < '0' || r > '9') {
					break
				}

				if len(nums) == 0 {
					nums = append(nums, 0)
				}
				if r == ';' {
					nums = append(nums, 0)
				} else {
					cur := len(nums) - 1
					nums[cur] = nums[cur] * 10 + int(r - '0')
				}
			}
			return parseCSI(nums, r)
		case 'O':
			// G3 style function key sequence: read one rune.
			r3, _, e := rd.buffed.ReadRune()
			if e == async.Timeout {
				return Key{r2, Alt}, nil
			} else if e != nil {
				return ZeroKey, e
			}
			r, ok := g3Seq[r3]
			if ok {
				return Key{r, 0}, nil
			} else {
				return ZeroKey, BadEscSeq
			}
		}
		return Key{r, Alt}, nil
	default:
		// Sane Ctrl- sequences that agree with the keyboard...
		if 0x1 <= r && r <= 0x1d {
			k = Key{r+0x40, Ctrl}
		} else {
			k = Key{r, 0}
		}
	}
	return
}

var keyByLast = map[rune]rune{
	'A': Up, 'B': Down, 'C': Right, 'D': Left,
	'H': Home, 'F': End,
}

// last == '~'
var keyByNum0 = map[int]rune{
	1: Home, 2: Insert, 3: Delete, 4: End, 5: PageUp, 6: PageDown,
	11: F1, 12: F2, 13: F3, 14: F4,
	15: F5, 17: F6, 18: F7, 19: F8, 20: F9, 21: F10, 23: F11, 24: F12,
}

// last == '~', num[0] == 27
// The list is taken blindly from tmux source xterm-keys.c. I don't have a
// keyboard that can generate such sequences, but assumably some PC keyboard
// with a numpad can.
var keyByNum2 = map[int]rune{
	9: '\t', 13: '\r',
	33: '!', 35: '#', 39: '\'', 40: '(', 41: ')', 43: '+', 44: ',', 45: '-',
	46: '.',
	48: '0', 49: '1', 50: '2', 51: '3', 52: '4', 53: '5', 54: '6', 55: '7',
	56: '8', 57: '9',
	58: ':', 59: ';', 60: '<', 61: '=', 62: '>', 63: ';',
}

// Parse a CSI-style function key sequence.
func parseCSI(nums []int, last rune) (Key, error) {
	if len(nums) != 0 && len(nums) != 2 {
		return ZeroKey, BadEscSeq
	}

	if r, ok := keyByLast[last]; ok {
		k := Key{r, 0}
		if len(nums) == 0 {
			return k, nil
		} else if len(nums) != 2 || nums[0] != 1 {
			return ZeroKey, BadEscSeq
		}
		return xtermModify(k, nums[1])
	}

	if last == '~' {
		if len(nums) == 1 || len(nums) == 2 {
			if r, ok := keyByNum0[nums[0]]; ok {
				k := Key{r, 0}
				if len(nums) == 2 {
					return xtermModify(k, nums[1])
				}
				return k, nil
			}
		} else if len(nums) == 3 && nums[0] == 27 {
			if r, ok := keyByNum2[nums[2]]; ok {
				k := Key{r, 0}
				return xtermModify(k, nums[1])
			}
		}
	}

	return ZeroKey, BadEscSeq
}

func xtermModify(k Key, mod int) (Key, error) {
	switch mod {
	case 0:
		// do nothing
	case 2:
		k.Mod |= Shift
	case 3:
		k.Mod |= Alt
	case 4:
		k.Mod |= Shift | Alt
	case 5:
		k.Mod |= Ctrl
	case 6:
		k.Mod |= Shift | Ctrl
	case 7:
		k.Mod |= Alt | Ctrl
	case 8:
		k.Mod |= Shift | Alt | Ctrl
	default:
		return ZeroKey, BadEscSeq
	}
	return k, nil
}
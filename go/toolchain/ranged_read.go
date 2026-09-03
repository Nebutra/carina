package toolchain

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	DefaultRangeMaxReadBytes   int64 = 8 << 20
	DefaultRangeMaxOutputBytes int64 = 1 << 20
	DefaultRangeBufferBytes          = 4 << 10
)

var (
	ErrInvalidLineRange        = errors.New("invalid line range")
	ErrRangeReadBudgetExceeded = errors.New("line range read budget exceeded")
	ErrRangeOutputTooLarge     = errors.New("line range output exceeds limit")
	ErrFileChangedDuringRead   = errors.New("file changed during ranged read")
)

type LineRangeLimits struct {
	MaxReadBytes   int64
	MaxOutputBytes int64
	BufferBytes    int
}

type LineRangeResult struct {
	Content   []byte
	StartLine int
	EndLine   int
	StartByte int64
	EndByte   int64
	BytesRead int64
	EOF       bool
	Truncated bool
}

type FileVersion struct {
	Size            int64
	Mode            uint32
	ModTimeUnixNano int64
}

type budgetReader struct {
	reader    io.Reader
	remaining int64
	read      int64
}

type prefixValidatingReader struct {
	reader    io.Reader
	remaining int
	prefix    []byte
	validate  func([]byte) error
	done      bool
}

func (r *prefixValidatingReader) Read(p []byte) (int, error) {
	if r.done {
		return r.reader.Read(p)
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.prefix = append(r.prefix, p[:n]...)
	r.remaining -= n
	if r.remaining == 0 || err != nil {
		r.done = true
		if validationErr := r.validate(r.prefix); validationErr != nil {
			// Do not expose prefix bytes together with a rejection: bufio may
			// otherwise satisfy a short line from those bytes and defer the
			// validation error until a later read the caller never performs.
			return 0, validationErr
		}
	}
	return n, err
}

func (r *budgetReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, ErrRangeReadBudgetExceeded
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, err
}

func normalizeLineRangeLimits(limits LineRangeLimits) (LineRangeLimits, error) {
	if limits.MaxReadBytes == 0 {
		limits.MaxReadBytes = DefaultRangeMaxReadBytes
	}
	if limits.MaxOutputBytes == 0 {
		limits.MaxOutputBytes = DefaultRangeMaxOutputBytes
	}
	if limits.BufferBytes == 0 {
		limits.BufferBytes = DefaultRangeBufferBytes
	}
	if limits.MaxReadBytes < 0 || limits.MaxOutputBytes < 0 || limits.BufferBytes < 1 {
		return LineRangeLimits{}, fmt.Errorf("%w: limits must be positive", ErrInvalidLineRange)
	}
	return limits, nil
}

// ReadLineRange returns complete logical lines while retaining only the
// requested window. bufio may fetch at most one fixed-size buffer beyond the
// logical end to establish EOF/truncation evidence; the unread tail is never
// accumulated or digested.
func ReadLineRange(reader io.Reader, startLine, lineCount int, limits LineRangeLimits) (result LineRangeResult, err error) {
	if startLine < 1 || lineCount < 1 {
		return result, fmt.Errorf("%w: start_line and line_count must be positive", ErrInvalidLineRange)
	}
	limits, err = normalizeLineRangeLimits(limits)
	if err != nil {
		return result, err
	}
	bounded := &budgetReader{reader: reader, remaining: limits.MaxReadBytes}
	defer func() { result.BytesRead = bounded.read }()
	buffered := bufio.NewReaderSize(bounded, limits.BufferBytes)
	result.StartLine = startLine
	result.EndLine = startLine - 1

	var logicalOffset int64
	for line := 1; line < startLine; line++ {
		consumed, present, eof, consumeErr := consumeLine(buffered, nil, limits.MaxOutputBytes)
		logicalOffset += consumed
		if consumeErr != nil {
			return result, consumeErr
		}
		if !present || eof {
			result.StartByte = logicalOffset
			result.EndByte = logicalOffset
			result.EOF = true
			return result, nil
		}
	}
	result.StartByte = logicalOffset

	for offset := 0; offset < lineCount; offset++ {
		consumed, present, eof, consumeErr := consumeLine(buffered, &result.Content, limits.MaxOutputBytes)
		logicalOffset += consumed
		if consumeErr != nil {
			return result, consumeErr
		}
		if !present {
			result.EOF = true
			break
		}
		result.EndLine = startLine + offset
		if eof {
			result.EOF = true
			break
		}
	}
	result.EndByte = logicalOffset
	if result.EOF || result.EndLine < startLine+lineCount-1 {
		return result, nil
	}
	if _, peekErr := buffered.Peek(1); peekErr == nil {
		result.Truncated = true
	} else if errors.Is(peekErr, io.EOF) {
		result.EOF = true
	} else {
		return result, peekErr
	}
	return result, nil
}

func consumeLine(reader *bufio.Reader, output *[]byte, maxOutput int64) (consumed int64, present, eof bool, err error) {
	for {
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			present = true
			consumed += int64(len(fragment))
			if output != nil {
				if int64(len(*output))+int64(len(fragment)) > maxOutput {
					return consumed, present, false, ErrRangeOutputTooLarge
				}
				*output = append(*output, fragment...)
			}
		}
		switch {
		case readErr == nil:
			return consumed, present, false, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			return consumed, present, true, nil
		default:
			return consumed, present, false, readErr
		}
	}
}

func ReadLineRangeFile(path string, startLine, lineCount int, limits LineRangeLimits) (LineRangeResult, FileVersion, error) {
	return readLineRangeFile(path, startLine, lineCount, limits, 0, nil, nil)
}

// ReadLineRangeFileValidated validates a bounded prefix from the same open
// file used for the range, eliminating classification/read TOCTOU windows.
func ReadLineRangeFileValidated(path string, startLine, lineCount int, limits LineRangeLimits, prefixBytes int, validate func([]byte) error) (LineRangeResult, FileVersion, error) {
	if prefixBytes < 1 || validate == nil {
		return LineRangeResult{}, FileVersion{}, fmt.Errorf("%w: prefix validator and positive prefix size are required", ErrInvalidLineRange)
	}
	return readLineRangeFile(path, startLine, lineCount, limits, prefixBytes, validate, nil)
}

func readLineRangeFile(path string, startLine, lineCount int, limits LineRangeLimits, prefixBytes int, validate func([]byte) error, afterRead func()) (LineRangeResult, FileVersion, error) {
	file, err := os.Open(path)
	if err != nil {
		return LineRangeResult{}, FileVersion{}, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return LineRangeResult{}, FileVersion{}, err
	}
	if !before.Mode().IsRegular() {
		return LineRangeResult{}, FileVersion{}, fmt.Errorf("ranged read requires a regular file")
	}
	version := FileVersion{Size: before.Size(), Mode: uint32(before.Mode()), ModTimeUnixNano: before.ModTime().UnixNano()}
	reader := io.Reader(file)
	if validate != nil {
		reader = &prefixValidatingReader{reader: file, remaining: prefixBytes, validate: validate}
	}
	result, readErr := ReadLineRange(reader, startLine, lineCount, limits)
	if readErr != nil {
		return result, version, readErr
	}
	if afterRead != nil {
		afterRead()
	}
	after, err := file.Stat()
	if err != nil {
		return result, version, err
	}
	pathAfter, err := os.Stat(path)
	if err != nil {
		return result, version, ErrFileChangedDuringRead
	}
	if !os.SameFile(before, after) || !os.SameFile(before, pathAfter) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return result, version, ErrFileChangedDuringRead
	}
	return result, version, nil
}

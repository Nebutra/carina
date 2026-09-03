package toolchain

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type countedReader struct {
	r io.Reader
	n int
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += n
	return n, err
}

type failAfterReader struct {
	data []byte
	n    int
	err  error
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if r.n >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.n:])
	r.n += n
	return n, nil
}

func TestReadLineRangeStopsBeforeUnreadTail(t *testing.T) {
	tail := strings.Repeat("unread\n", 1_000_000)
	source := &countedReader{r: strings.NewReader("one\ntwo\n" + tail)}
	result, err := ReadLineRange(source, 1, 2, LineRangeLimits{
		MaxReadBytes:   1 << 20,
		MaxOutputBytes: 1 << 20,
		BufferBytes:    32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Content); got != "one\ntwo\n" {
		t.Fatalf("content = %q", got)
	}
	if !result.Truncated || result.EOF {
		t.Fatalf("range evidence = %+v", result)
	}
	if source.n > len(result.Content)+32 {
		t.Fatalf("read %d bytes for an %d-byte window; lookahead exceeded buffer", source.n, len(result.Content))
	}
}

func TestReadLineRangeBoundaries(t *testing.T) {
	tests := []struct {
		name              string
		input             []byte
		start, count      int
		want              []byte
		wantStartByte     int64
		wantEndLine       int
		wantEOF, wantMore bool
	}{
		{name: "empty", start: 1, count: 1, wantEOF: true},
		{name: "beyond eof", input: []byte("one\n"), start: 3, count: 2, wantStartByte: 4, wantEndLine: 2, wantEOF: true},
		{name: "final unterminated", input: []byte("one\ntwo"), start: 2, count: 2, want: []byte("two"), wantStartByte: 4, wantEndLine: 2, wantEOF: true},
		{name: "crlf exact", input: []byte("one\r\ntwo\r\nthree\r\n"), start: 2, count: 1, want: []byte("two\r\n"), wantStartByte: 5, wantEndLine: 2, wantMore: true},
		{name: "multibyte tiny buffer", input: []byte("甲\n乙\n丙\n"), start: 2, count: 1, want: []byte("乙\n"), wantStartByte: 4, wantEndLine: 2, wantMore: true},
		{name: "invalid utf8 stays raw", input: []byte{'a', '\n', 0xff, '\n'}, start: 2, count: 1, want: []byte{0xff, '\n'}, wantStartByte: 2, wantEndLine: 2, wantEOF: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ReadLineRange(bytes.NewReader(test.input), test.start, test.count, LineRangeLimits{
				MaxReadBytes: 128, MaxOutputBytes: 64, BufferBytes: 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(result.Content, test.want) || result.StartByte != test.wantStartByte || result.EndLine != test.wantEndLine || result.EOF != test.wantEOF || result.Truncated != test.wantMore {
				t.Fatalf("result = %+v content=%q", result, result.Content)
			}
		})
	}
}

func TestReadLineRangeRejectsInvalidArgumentsAndHugeLines(t *testing.T) {
	if _, err := ReadLineRange(strings.NewReader("x\n"), 0, 1, LineRangeLimits{}); !errors.Is(err, ErrInvalidLineRange) {
		t.Fatalf("start_line error = %v", err)
	}
	if _, err := ReadLineRange(strings.NewReader("x\n"), 1, 0, LineRangeLimits{}); !errors.Is(err, ErrInvalidLineRange) {
		t.Fatalf("line_count error = %v", err)
	}
	if _, err := ReadLineRange(strings.NewReader(strings.Repeat("x", 128)+"\n"), 1, 1, LineRangeLimits{
		MaxReadBytes: 256, MaxOutputBytes: 32, BufferBytes: 8,
	}); !errors.Is(err, ErrRangeOutputTooLarge) {
		t.Fatalf("huge-line error = %v", err)
	}
	if _, err := ReadLineRange(strings.NewReader(strings.Repeat("skip", 64)+"\nkeep\n"), 2, 1, LineRangeLimits{
		MaxReadBytes: 32, MaxOutputBytes: 32, BufferBytes: 8,
	}); !errors.Is(err, ErrRangeReadBudgetExceeded) {
		t.Fatalf("read-budget error = %v", err)
	}
}

func TestReadLineRangePropagatesReaderFailure(t *testing.T) {
	want := errors.New("injected read failure")
	reader := &failAfterReader{data: []byte("one\ntw"), err: want}
	_, err := ReadLineRange(reader, 2, 1, LineRangeLimits{
		MaxReadBytes: 128, MaxOutputBytes: 64, BufferBytes: 2,
	})
	if !errors.Is(err, want) {
		t.Fatalf("reader failure = %v", err)
	}
}

func TestReadLineRangeFileDetectsChangeAndValidatesSameOpenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := readLineRangeFile(path, 1, 1, LineRangeLimits{}, 0, nil, func() {
		if writeErr := os.WriteFile(path, []byte("changed\ntwo\nthree\n"), 0o600); writeErr != nil {
			t.Errorf("mutate file: %v", writeErr)
		}
	})
	if !errors.Is(err, ErrFileChangedDuringRead) {
		t.Fatalf("change detection = %v", err)
	}
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = readLineRangeFile(path, 1, 1, LineRangeLimits{}, 0, nil, func() {
		replacement := path + ".replacement"
		if writeErr := os.WriteFile(replacement, []byte("replacement\n"), 0o600); writeErr != nil {
			t.Errorf("write replacement: %v", writeErr)
			return
		}
		if renameErr := os.Rename(replacement, path); renameErr != nil {
			t.Errorf("replace file: %v", renameErr)
		}
	})
	if !errors.Is(err, ErrFileChangedDuringRead) {
		t.Fatalf("identity replacement detection = %v", err)
	}
	if err := os.WriteFile(path, []byte("changed\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	want := errors.New("unsupported prefix")
	seen := []byte(nil)
	_, _, err = ReadLineRangeFileValidated(path, 1, 1, LineRangeLimits{BufferBytes: 64}, 4, func(prefix []byte) error {
		seen = append([]byte(nil), prefix...)
		return want
	})
	if !errors.Is(err, want) || string(seen) != "chan" {
		t.Fatalf("prefix validation err=%v prefix=%q", err, seen)
	}
}

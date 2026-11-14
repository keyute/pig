package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/keyute/kopi/internal/policy"
)

const (
	maxReadLines   = 2000
	maxLineChars   = 2000
	maxReadBytes   = 100 << 10
	binarySniffLen = 8 << 10
	// noticeReserve keeps room under maxReadBytes for the final notice line.
	noticeReserve = 256
	lineCutMarker = " [line truncated]"
)

type readArgs struct {
	FilePath string `json:"file_path"`
	Offset   *int   `json:"offset"`
	Limit    *int   `json:"limit"`
}

func NewRead(ws *Workspace) Tool {
	return Tool{
		Name:  "Read",
		Label: "Read",
		Description: "Reads a text file inside the workspace. file_path is absolute or relative to the workspace. " +
			"Returns numbered lines, up to 2000 lines by default; use offset and limit to read other ranges.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path of the file to read, absolute or relative to the workspace.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "1-based line number to start reading from.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Maximum number of lines to read (at most 2000).",
				},
			},
			"required":             []string{"file_path"},
			"additionalProperties": false,
		},
		Capability: policy.Read,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			args, err := decodeReadArgs(raw)
			if err != nil {
				return "", err
			}
			return read(ctx, ws, args)
		},
	}
}

func decodeReadArgs(raw json.RawMessage) (readArgs, error) {
	var args readArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return args, fmt.Errorf("%w: %w", ErrInvalidArgs, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return args, fmt.Errorf("%w: trailing data after arguments", ErrInvalidArgs)
	}
	if args.FilePath == "" {
		return args, fmt.Errorf("%w: file_path is required", ErrInvalidArgs)
	}
	if args.Offset != nil && *args.Offset < 1 {
		return args, fmt.Errorf("%w: offset must be at least 1", ErrInvalidArgs)
	}
	if args.Limit != nil && *args.Limit < 1 {
		return args, fmt.Errorf("%w: limit must be at least 1", ErrInvalidArgs)
	}
	return args, nil
}

func read(ctx context.Context, ws *Workspace, args readArgs) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	offset, limit := 1, maxReadLines
	if args.Offset != nil {
		offset = *args.Offset
	}
	if args.Limit != nil {
		limit = min(*args.Limit, maxReadLines)
	}

	f, err := ws.Open(args.FilePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat %s: %w", args.FilePath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrIsDirectory, args.FilePath)
	}

	r := bufio.NewReaderSize(f, 64<<10)
	head, err := r.Peek(binarySniffLen)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("failed to read %s: %w", args.FilePath, err)
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return "", fmt.Errorf("%w: %s", ErrBinary, args.FilePath)
	}
	if len(head) == 0 {
		return "(empty file)", nil
	}

	var out strings.Builder
	lineNo, last := 0, 0
	more := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		line, cut, err := readLine(ctx, r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to read %s: %w", args.FilePath, err)
		}
		lineNo++
		if lineNo < offset {
			continue
		}
		if last-offset+1 >= limit {
			more = true
			break
		}
		formatted := fmt.Sprintf("%6d\t%s", lineNo, line)
		if cut {
			formatted += lineCutMarker
		}
		formatted += "\n"
		if out.Len()+len(formatted) > maxReadBytes-noticeReserve {
			more = true
			break
		}
		out.WriteString(formatted)
		last = lineNo
	}

	if last == 0 {
		return fmt.Sprintf("(offset %d is past the end of the file, which has %d lines)", offset, lineNo), nil
	}
	if more {
		fmt.Fprintf(&out, "[Showing lines %d-%d. More lines remain; use offset=%d to continue.]\n", offset, last, last+1)
	}
	return out.String(), nil
}

// readLine returns the next line without its newline, keeping at most
// maxLineChars characters and discarding the rest so an unbounded line never
// sits in memory. cut reports whether characters were dropped. io.EOF is
// returned only when no further line exists.
func readLine(ctx context.Context, r *bufio.Reader) (line string, cut bool, err error) {
	// A character is at most utf8.UTFMax bytes, so this many bytes always
	// hold maxLineChars characters plus one to detect the overflow.
	const keep = (maxLineChars + 1) * utf8.UTFMax
	var buf []byte
	got := false
	for {
		chunk, err := r.ReadSlice('\n')
		got = got || len(chunk) > 0
		room := max(keep-len(buf), 0)
		buf = append(buf, chunk[:min(len(chunk), room)]...)
		if errors.Is(err, bufio.ErrBufferFull) {
			if err := ctx.Err(); err != nil {
				return "", false, err
			}
			continue
		}
		if err != nil && (!errors.Is(err, io.EOF) || !got) {
			return "", false, err
		}
		break
	}
	buf = bytes.TrimSuffix(buf, []byte("\n"))
	n, chars := 0, 0
	for n < len(buf) && chars < maxLineChars {
		_, size := utf8.DecodeRune(buf[n:])
		n += size
		chars++
	}
	return string(buf[:n]), n < len(buf), nil
}

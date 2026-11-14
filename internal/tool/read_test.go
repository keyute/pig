package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/keyute/kopi/internal/policy"
)

func newReadTool(t *testing.T) (Tool, string) {
	t.Helper()
	dir := t.TempDir()
	return NewRead(newWorkspace(t, dir, nil)), dir
}

func runRead(t *testing.T, tool Tool, args string) (string, error) {
	t.Helper()
	return tool.Run(context.Background(), json.RawMessage(args))
}

func numberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestReadDescribesItself(t *testing.T) {
	tool, _ := newReadTool(t)

	assert.Equal(t, "Read", tool.Name)
	assert.Equal(t, policy.Read, tool.Capability)
	assert.Equal(t, "object", tool.Parameters["type"])
	assert.Equal(t, []string{"file_path"}, tool.Parameters["required"])
	assert.Equal(t, false, tool.Parameters["additionalProperties"])
	_, err := json.Marshal(tool.Parameters)
	require.NoError(t, err)
}

func TestReadNumbersLines(t *testing.T) {
	tool, dir := newReadTool(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\nbeta\n\ngamma")

	out, err := runRead(t, tool, `{"file_path":"a.txt"}`)
	require.NoError(t, err)
	assert.Equal(t, "     1\talpha\n     2\tbeta\n     3\t\n     4\tgamma\n", out)
}

func TestReadHonoursOffsetAndLimit(t *testing.T) {
	tool, dir := newReadTool(t)
	writeFile(t, filepath.Join(dir, "a.txt"), numberedLines(10))

	for _, tc := range []struct {
		name, args, want string
	}{
		{
			name: "offset to end",
			args: `{"file_path":"a.txt","offset":9}`,
			want: "     9\tline 9\n    10\tline 10\n",
		},
		{
			name: "offset and limit",
			args: `{"file_path":"a.txt","offset":3,"limit":2}`,
			want: "     3\tline 3\n     4\tline 4\n" +
				"[Showing lines 3-4. More lines remain; use offset=5 to continue.]\n",
		},
		{
			name: "limit reaching the last line",
			args: `{"file_path":"a.txt","offset":8,"limit":3}`,
			want: "     8\tline 8\n     9\tline 9\n    10\tline 10\n",
		},
		{
			name: "offset past end",
			args: `{"file_path":"a.txt","offset":11}`,
			want: "(offset 11 is past the end of the file, which has 10 lines)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runRead(t, tool, tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.want, out)
		})
	}
}

func TestReadStopsAtLineLimitWithNotice(t *testing.T) {
	tool, dir := newReadTool(t)
	writeFile(t, filepath.Join(dir, "a.txt"), numberedLines(maxReadLines+5))

	for _, args := range []string{
		`{"file_path":"a.txt"}`,
		`{"file_path":"a.txt","limit":5000}`,
	} {
		out, err := runRead(t, tool, args)
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		require.Len(t, lines, maxReadLines+1)
		assert.Equal(t, "  2000\tline 2000", lines[maxReadLines-1])
		assert.Equal(t, "[Showing lines 1-2000. More lines remain; use offset=2001 to continue.]", lines[maxReadLines])
	}
}

func TestReadCutsLongLines(t *testing.T) {
	tool, dir := newReadTool(t)
	long := strings.Repeat("é", maxLineChars+50)
	writeFile(t, filepath.Join(dir, "a.txt"), long+"\nnext\n")

	out, err := runRead(t, tool, `{"file_path":"a.txt"}`)
	require.NoError(t, err)
	want := "     1\t" + strings.Repeat("é", maxLineChars) + lineCutMarker + "\n     2\tnext\n"
	assert.Equal(t, want, out)
}

func TestReadCutsUnboundedLine(t *testing.T) {
	tool, dir := newReadTool(t)
	writeFile(t, filepath.Join(dir, "a.txt"), strings.Repeat("x", 1<<20)+"\nnext")

	out, err := runRead(t, tool, `{"file_path":"a.txt"}`)
	require.NoError(t, err)
	want := "     1\t" + strings.Repeat("x", maxLineChars) + lineCutMarker + "\n     2\tnext\n"
	assert.Equal(t, want, out)
}

func TestReadStopsAtByteCapWithNotice(t *testing.T) {
	tool, dir := newReadTool(t)
	line := strings.Repeat("y", 1000)
	writeFile(t, filepath.Join(dir, "a.txt"), strings.Repeat(line+"\n", 500))

	out, err := runRead(t, tool, `{"file_path":"a.txt"}`)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(out), maxReadBytes)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	shown := len(lines) - 1
	assert.Less(t, shown, 500)
	assert.Equal(t, fmt.Sprintf("[Showing lines 1-%d. More lines remain; use offset=%d to continue.]", shown, shown+1), lines[shown])
}

func TestReadReturnsNoticeForEmptyFile(t *testing.T) {
	tool, dir := newReadTool(t)
	writeFile(t, filepath.Join(dir, "empty.txt"), "")

	out, err := runRead(t, tool, `{"file_path":"empty.txt"}`)
	require.NoError(t, err)
	assert.Equal(t, "(empty file)", out)
}

func TestReadRefusesUnreadableTargets(t *testing.T) {
	tool, dir := newReadTool(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	writeFile(t, filepath.Join(dir, "bin"), "ELF\x00\x01\x02")

	for _, tc := range []struct {
		name, path string
		want       error
	}{
		{"directory", "sub", ErrIsDirectory},
		{"binary", "bin", ErrBinary},
		{"missing", "missing.txt", fs.ErrNotExist},
		{"outside", "../x", ErrOutsideWorkspace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runRead(t, tool, fmt.Sprintf(`{"file_path":%q}`, tc.path))
			require.ErrorIs(t, err, tc.want)
			assert.Empty(t, out)
		})
	}
}

func TestReadRejectsInvalidArgs(t *testing.T) {
	tool, dir := newReadTool(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\n")

	for _, tc := range []struct {
		name, args string
	}{
		{"bad json", `{"file_path":`},
		{"not an object", `"a.txt"`},
		{"trailing data", `{"file_path":"a.txt"} {}`},
		{"unknown field", `{"file_path":"a.txt","pattern":"x"}`},
		{"missing path", `{}`},
		{"empty path", `{"file_path":""}`},
		{"offset zero", `{"file_path":"a.txt","offset":0}`},
		{"limit zero", `{"file_path":"a.txt","limit":0}`},
		{"negative limit", `{"file_path":"a.txt","limit":-1}`},
		{"path wrong type", `{"file_path":1}`},
		{"offset wrong type", `{"file_path":"a.txt","offset":"1"}`},
		{"fractional limit", `{"file_path":"a.txt","limit":1.5}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runRead(t, tool, tc.args)
			require.ErrorIs(t, err, ErrInvalidArgs)
		})
	}
}

func TestReadReturnsContextErrorWhenCancelled(t *testing.T) {
	tool, dir := newReadTool(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tool.Run(ctx, json.RawMessage(`{"file_path":"a.txt"}`))
	require.ErrorIs(t, err, context.Canceled)
}

func TestReadLineStopsDiscardingWhenCancelled(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 1<<10)), 16)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := readLine(ctx, r)
	require.ErrorIs(t, err, context.Canceled)
}

package textutil

import "bytes"

// NormalizeUTF8LF converts CRLF to LF and ensures the output is valid UTF-8
// by replacing invalid byte sequences with the Unicode replacement character.
func NormalizeUTF8LF(b []byte) []byte {
	// Normalize newlines first
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	// Ensure valid UTF-8
	return bytes.ToValidUTF8(b, []byte("\uFFFD"))
}

// EnsureTrailingLF appends a single \n if not already present.
func EnsureTrailingLF(b []byte) []byte {
	if len(b) == 0 || b[len(b)-1] == '\n' {
		return b
	}
	return append(b, '\n')
}

// JoinWithSingleNL concatenates chunks, inserting a single '\n' between
// chunks when the previous chunk does not end with '\n'.
func JoinWithSingleNL(chunks ...[]byte) []byte {
	if len(chunks) == 0 {
		return nil
	}
	var out []byte
	for i, c := range chunks {
		if i > 0 && len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, c...)
	}
	return out
}

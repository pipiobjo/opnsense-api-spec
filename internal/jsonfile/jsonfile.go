// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package jsonfile reads and writes the JSON files of dist/ and of the
// capability data in one format: indented by two spaces, no HTML
// escaping, a final newline. Deterministic for the same value.
package jsonfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Read decodes the file at path into v; the error names the file.
func Read(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Write encodes v into the file at path.
func Write(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

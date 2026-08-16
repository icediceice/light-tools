package symbol

import "errors"

var ErrUnavailable = errors.New("tree-sitter support is not included in this build")

type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Signature string `json:"signature,omitempty"`
	Comment   string `json:"comment,omitempty"`
	Parent    string `json:"parent,omitempty"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	StartByte uint   `json:"start_byte"`
	EndByte   uint   `json:"end_byte"`
}

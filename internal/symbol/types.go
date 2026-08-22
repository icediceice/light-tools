package symbol

import "errors"

var (
	ErrUnavailable        = errors.New("tree-sitter support is not included in this build")
	ErrUnsupportedExtension = errors.New("unsupported symbol extension")
	ErrParseHostile       = errors.New("source rejected as parse-hostile")
	ErrParseTimeout       = errors.New("tree-sitter parse timed out")
	ErrParseBusy          = errors.New("tree-sitter parser is recovering from a timed-out parse")
)

const (
	KindFunction     = "function"
	KindMethod       = "method"
	KindClass        = "class"
	KindInterface    = "interface"
	KindEnum         = "enum"
	KindRecord       = "record"
	KindStruct       = "struct"
	KindTrait        = "trait"
	KindObject       = "object"
	KindType         = "type"
	KindConst        = "const"
	KindModule       = "module"
	KindSymbol       = "symbol"
	KindHTMLID       = "html_id"
	KindHTMLName     = "html_name"
	KindHTMLHandler  = "html_handler"
	KindHTMLText     = "html_text"
	KindCSSClass     = "css_class"
	KindCSSID        = "css_id"
	KindCSSKeyframes = "css_keyframes"
	KindCSSElement   = "css_element"
	KindMDHeading    = "md_heading"
	KindYAMLKey      = "yaml_key"
	KindTOMLSection  = "toml_section"
	KindTOMLKey      = "toml_key"
)

var validKinds = map[string]struct{}{
	KindFunction: {}, KindMethod: {}, KindClass: {}, KindInterface: {},
	KindEnum: {}, KindRecord: {}, KindStruct: {}, KindTrait: {},
	KindObject: {}, KindType: {}, KindConst: {}, KindModule: {}, KindSymbol: {},
	KindHTMLID: {}, KindHTMLName: {}, KindHTMLHandler: {}, KindHTMLText: {},
	KindCSSClass: {}, KindCSSID: {}, KindCSSKeyframes: {}, KindCSSElement: {},
	KindMDHeading: {}, KindYAMLKey: {}, KindTOMLSection: {}, KindTOMLKey: {},
}

func ValidKind(kind string) bool {
	_, ok := validKinds[kind]
	return ok
}

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

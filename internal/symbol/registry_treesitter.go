//go:build treesitter

package symbol

import "unsafe"

import (
	dart "github.com/UserNobody14/tree-sitter-dart/bindings/go"
	kotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
	lua "github.com/tree-sitter-grammars/tree-sitter-lua/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	bash "github.com/tree-sitter/tree-sitter-bash/bindings/go"
	csharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	clang "github.com/tree-sitter/tree-sitter-c/bindings/go"
	cpplang "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	html "github.com/tree-sitter/tree-sitter-html/bindings/go"
	java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	phplang "github.com/tree-sitter/tree-sitter-php/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	scala "github.com/tree-sitter/tree-sitter-scala/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

type grammarDescriptor struct {
	id       languageID
	language func() *tree_sitter.Language
	query    string
	kinds    map[string]string
	noise    map[string]bool
	special  func(*tree_sitter.Node, []byte) []Symbol
}

func language(raw unsafeLanguage) *tree_sitter.Language {
	return tree_sitter.NewLanguage(raw())
}

type unsafeLanguage func() unsafe.Pointer

var _ = language

func grammarFor(id languageID) (grammarDescriptor, bool) {
	descriptor, ok := grammarRegistry[id]
	return descriptor, ok
}

func languageFrom(raw func() unsafe.Pointer) func() *tree_sitter.Language {
	return func() *tree_sitter.Language { return tree_sitter.NewLanguage(raw()) }
}

var grammarRegistry = map[languageID]grammarDescriptor{
	langGo: {
		id: langGo, language: languageFrom(golang.Language), query: `
(function_declaration name: (identifier) @name) @body
(method_declaration name: (field_identifier) @name) @body
(type_declaration (type_spec name: (type_identifier) @name) @body)
`, kinds: map[string]string{"function_declaration": KindFunction, "method_declaration": KindMethod, "type_spec": KindType},
	},
	langJavaScript: {
		id: langJavaScript, language: languageFrom(javascript.Language), query: `
(function_declaration name: (identifier) @name) @body
(method_definition name: (property_identifier) @name) @body
(class_declaration name: (identifier) @name) @body
(lexical_declaration (variable_declarator name: (identifier) @name value: [(arrow_function) (function_expression)])) @body
(variable_declaration (variable_declarator name: (identifier) @name value: [(arrow_function) (function_expression)])) @body
(assignment_expression left: (member_expression property: (property_identifier) @name) right: [(arrow_function) (function_expression)]) @body
(call_expression function: [(identifier) (member_expression)] arguments: (arguments [(string) (template_string)] @name [(arrow_function) (function_expression)])) @body
(pair key: (property_identifier) @name value: [(arrow_function) (function_expression)]) @body
`, kinds: map[string]string{
			"function_declaration": KindFunction, "method_definition": KindMethod, "class_declaration": KindClass,
			"lexical_declaration": KindFunction, "variable_declaration": KindFunction,
			"assignment_expression": KindFunction, "call_expression": KindFunction, "pair": KindMethod,
		}, noise: map[string]bool{"assignment_expression": true, "call_expression": true, "pair": true},
	},
	langTypeScript: {
		id: langTypeScript, language: languageFrom(typescript.LanguageTypescript), query: typeScriptQuery,
		kinds: typeScriptKinds, noise: map[string]bool{"pair": true},
	},
	langTSX: {
		id: langTSX, language: languageFrom(typescript.LanguageTSX), query: typeScriptQuery,
		kinds: typeScriptKinds, noise: map[string]bool{"pair": true},
	},
	langPython: {
		id: langPython, language: languageFrom(python.Language), query: `
(function_definition name: (identifier) @name) @body
(class_definition name: (identifier) @name) @body
`, kinds: map[string]string{"function_definition": KindFunction, "class_definition": KindClass},
	},
	langJava: {
		id: langJava, language: languageFrom(java.Language), query: `
(method_declaration name: (identifier) @name) @body
(class_declaration name: (identifier) @name) @body
(interface_declaration name: (identifier) @name) @body
(enum_declaration name: (identifier) @name) @body
(record_declaration name: (identifier) @name) @body
`, kinds: map[string]string{
			"method_declaration": KindMethod, "class_declaration": KindClass,
			"interface_declaration": KindInterface, "enum_declaration": KindEnum, "record_declaration": KindRecord,
		},
	},
	langRust: {
		id: langRust, language: languageFrom(rust.Language), query: `
(function_item name: (identifier) @name) @body
(struct_item name: (type_identifier) @name) @body
(enum_item name: (type_identifier) @name) @body
(trait_item name: (type_identifier) @name) @body
`, kinds: map[string]string{"function_item": KindFunction, "struct_item": KindStruct, "enum_item": KindEnum, "trait_item": KindTrait},
	},
	langC: {
		id: langC, language: languageFrom(clang.Language), query: `
(function_definition declarator: (function_declarator declarator: (identifier) @name)) @body
(struct_specifier name: (type_identifier) @name) @body
(enum_specifier name: (type_identifier) @name) @body
`, kinds: map[string]string{"function_definition": KindFunction, "struct_specifier": KindStruct, "enum_specifier": KindEnum},
	},
	langCPP: {
		id: langCPP, language: languageFrom(cpplang.Language), query: `
(function_definition declarator: (function_declarator declarator: (identifier) @name)) @body
(class_specifier name: (type_identifier) @name) @body
(struct_specifier name: (type_identifier) @name) @body
(enum_specifier name: (type_identifier) @name) @body
`, kinds: map[string]string{
			"function_definition": KindFunction, "class_specifier": KindClass,
			"struct_specifier": KindStruct, "enum_specifier": KindEnum,
		},
	},
	langCSharp: {
		id: langCSharp, language: languageFrom(csharp.Language), query: `
(method_declaration name: (identifier) @name) @body
(class_declaration name: (identifier) @name) @body
(interface_declaration name: (identifier) @name) @body
(enum_declaration name: (identifier) @name) @body
`, kinds: map[string]string{
			"method_declaration": KindMethod, "class_declaration": KindClass,
			"interface_declaration": KindInterface, "enum_declaration": KindEnum,
		},
	},
	langRuby: {
		id: langRuby, language: languageFrom(ruby.Language), query: `
(method name: (identifier) @name) @body
(singleton_method name: (identifier) @name) @body
(class name: (constant) @name) @body
(module name: (constant) @name) @body
`, kinds: map[string]string{"method": KindMethod, "singleton_method": KindMethod, "class": KindClass, "module": KindModule},
	},
	langPHP: {
		id: langPHP, language: languageFrom(phplang.LanguagePHP), query: `
(function_definition name: (name) @name) @body
(method_declaration name: (name) @name) @body
(class_declaration name: (name) @name) @body
(interface_declaration name: (name) @name) @body
`, kinds: map[string]string{
			"function_definition": KindFunction, "method_declaration": KindMethod,
			"class_declaration": KindClass, "interface_declaration": KindInterface,
		},
	},
	langBash: {
		id: langBash, language: languageFrom(bash.Language), query: `
(function_definition name: (word) @name) @body
`, kinds: map[string]string{"function_definition": KindFunction},
	},
	langLua: {
		id: langLua, language: languageFrom(lua.Language), query: `
(function_declaration name: (identifier) @name) @body
`, kinds: map[string]string{"function_declaration": KindFunction, "local_function_statement": KindFunction},
	},
	langScala: {
		id: langScala, language: languageFrom(scala.Language), query: `
(function_definition name: (identifier) @name) @body
(class_definition name: (identifier) @name) @body
(object_definition name: (identifier) @name) @body
(trait_definition name: (identifier) @name) @body
`, kinds: map[string]string{
			"function_definition": KindFunction, "class_definition": KindClass,
			"object_definition": KindObject, "trait_definition": KindTrait,
		},
	},
	langKotlin: {
		id: langKotlin, language: languageFrom(kotlin.Language), query: `
(function_declaration (identifier) @name) @body
(class_declaration (identifier) @name) @body
(object_declaration (identifier) @name) @body
`, kinds: map[string]string{
			"function_declaration": KindFunction, "class_declaration": KindClass, "object_declaration": KindObject,
		},
	},
	langDart: {
		id: langDart, language: languageFrom(dart.Language), special: extractDartSymbols,
	},
	langHTML: {
		id: langHTML, language: languageFrom(html.Language), special: extractHTMLSymbols,
	},
}

const typeScriptQuery = `
(function_declaration name: (identifier) @name) @body
(method_definition name: (property_identifier) @name) @body
(class_declaration name: (type_identifier) @name) @body
(interface_declaration name: (type_identifier) @name) @body
(enum_declaration name: (identifier) @name) @body
(type_alias_declaration name: (type_identifier) @name) @body
(lexical_declaration (variable_declarator name: (identifier) @name value: [(arrow_function) (function_expression)])) @body
`

var typeScriptKinds = map[string]string{
	"function_declaration": KindFunction, "method_definition": KindMethod,
	"class_declaration": KindClass, "interface_declaration": KindInterface,
	"enum_declaration": KindEnum, "type_alias_declaration": KindType, "lexical_declaration": KindFunction,
}

package file

import (
	"fmt"
	"strings"
)

type Verb string

const (
	VerbWrite   Verb = "write"
	VerbEdit    Verb = "edit"
	VerbSed     Verb = "sed"
	VerbRename  Verb = "rename"
	VerbRewrite Verb = "rewrite"
	VerbRestore Verb = "vault_restore"
)

// Mutation is the sole typed mutation IR. JSON and sealed payload requests must
// both produce this type before validation or execution.
type Mutation struct {
	Verb            Verb    `json:"verb"`
	Path            string  `json:"path"`
	Target          string  `json:"target,omitempty"`
	Content         *string `json:"content,omitempty"`
	NewString       *string `json:"new_string,omitempty"`
	Find            *string `json:"find,omitempty"`
	Replace         *string `json:"replace,omitempty"`
	StartLine       int     `json:"start_line,omitempty"`
	EndLine         int     `json:"end_line,omitempty"`
	StartGuard      string  `json:"start_guard,omitempty"`
	EndGuard        string  `json:"end_guard,omitempty"`
	All             bool    `json:"all,omitempty"`
	Count           int     `json:"count,omitempty"`
	Regex           bool    `json:"regex,omitempty"`
	DryRun          bool    `json:"dry_run,omitempty"`
	Overwrite       bool    `json:"overwrite,omitempty"`
	AllowUnbalanced bool    `json:"allow_unbalanced,omitempty"`
	ExpectedSHA     string  `json:"expected_sha,omitempty"`
	Version         int     `json:"version,omitempty"`
	Force           bool    `json:"force,omitempty"`
}

func (m Mutation) Validate() error {
	if strings.TrimSpace(m.Path) == "" {
		return fmt.Errorf("path is required")
	}
	switch m.Verb {
	case VerbWrite:
		if m.Content == nil {
			return fmt.Errorf("write requires content")
		}
	case VerbEdit:
		if m.StartLine < 1 || m.NewString == nil {
			return fmt.Errorf("edit requires start_line >= 1 and new_string")
		}
		if m.EndLine != 0 && m.EndLine < m.StartLine {
			return fmt.Errorf("end_line precedes start_line")
		}
	case VerbSed:
		if m.Find == nil || m.Replace == nil {
			return fmt.Errorf("sed requires find and replace")
		}
		if m.Count < 0 {
			return fmt.Errorf("count cannot be negative")
		}
	case VerbRename:
		if strings.TrimSpace(m.Target) == "" {
			return fmt.Errorf("rename requires target")
		}
	case VerbRewrite:
		if m.StartLine < 1 || m.NewString == nil {
			return fmt.Errorf("rewrite requires start_line and new_string")
		}
	case VerbRestore:
		if m.Version < 0 {
			return fmt.Errorf("version cannot be negative")
		}
	default:
		return fmt.Errorf("unsupported mutation verb %q", m.Verb)
	}
	return nil
}

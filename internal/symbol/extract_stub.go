//go:build !treesitter

package symbol

func Extract(string, []byte) ([]Symbol, error) { return nil, ErrUnavailable }

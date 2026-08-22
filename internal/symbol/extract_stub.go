//go:build !treesitter

package symbol

func Extract(path string, source []byte) ([]Symbol, error) {
	if symbols, ok := extractText(path, source); ok {
		return symbols, nil
	}
	if _, err := extensionFor(path); err != nil {
		return nil, err
	}
	return nil, ErrUnavailable
}

//go:build treesitter

package symbol

import (
	"fmt"
	"time"
)

const (
	parseDeadline       = 10 * time.Second
	parseMaxLineBytes   = 8000
	parseTimeoutMicros  = uint64(parseDeadline / time.Microsecond)
)

type parseResult struct {
	symbols []Symbol
	err     error
}

var parserCircuit = func() chan struct{} {
	slot := make(chan struct{}, 1)
	slot <- struct{}{}
	return slot
}()

func maxLineBytes(source []byte) int {
	longest := 0
	current := 0
	for _, value := range source {
		if value == '\n' {
			if current > longest {
				longest = current
			}
			current = 0
			continue
		}
		current++
	}
	if current > longest {
		longest = current
	}
	return longest
}

func runParserWork(deadline time.Duration, work func() ([]Symbol, error)) ([]Symbol, error) {
	select {
	case <-parserCircuit:
	default:
		return nil, ErrParseBusy
	}

	result := make(chan parseResult, 1)
	go func() {
		defer func() { parserCircuit <- struct{}{} }()
		symbols, err := work()
		result <- parseResult{symbols: symbols, err: err}
	}()

	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case completed := <-result:
		return completed.symbols, completed.err
	case <-timer.C:
		return nil, fmt.Errorf("%w after %s", ErrParseTimeout, deadline)
	}
}

package telemetry

import (
	"fmt"
	"testing"
)

func TestConcurrentRecordAndFlushDoesNotRace(t *testing.T) {
	store := newTestStore(t, t.TempDir())

	// Make JSON marshaling long enough to overlap a live RecordCall reliably.
	for index := 0; index < 20000; index++ {
		store.RecordCall(fmt.Sprintf("seed-%d", index))
	}

	started := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		for index := 0; ; index++ {
			select {
			case <-stop:
				return
			default:
				store.RecordCall(fmt.Sprintf("live-%d", index%257))
			}
		}
	}()
	<-started

	for index := 0; index < 8; index++ {
		store.RecordTerseTokens(1)
		store.flush()
	}
	close(stop)
	<-done
}

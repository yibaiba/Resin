package proxy

import (
	"errors"
	"testing"
)

func TestShouldRecordForwardCopyFailure(t *testing.T) {
	t.Run("NilError", func(t *testing.T) {
		if shouldRecordForwardCopyFailure(nil) {
			t.Fatal("nil copy error should not be recorded as failure")
		}
	})

	t.Run("ContextCanceledError", func(t *testing.T) {
		if !shouldRecordForwardCopyFailure(canceledErr{}) {
			t.Fatal("canceled transfer should remain a request failure")
		}
	})

	t.Run("UpstreamTimeout", func(t *testing.T) {
		if !shouldRecordForwardCopyFailure(deadlineExceededErr{}) {
			t.Fatal("upstream timeout should be recorded as request failure")
		}
	})

	t.Run("GenericUpstreamError", func(t *testing.T) {
		err := errors.New("upstream read failed")
		if !shouldRecordForwardCopyFailure(err) {
			t.Fatal("generic upstream read error should be recorded as request failure")
		}
	})
}

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type timeoutTestTx struct{}

func TestGetOrBeginTxWithTimeout_SuccessInvokesOnTxStartedAndDelegatesCancel(t *testing.T) {
	t.Parallel()

	var beginCtx context.Context
	var capturedCancel context.CancelFunc

	tx, err := getOrBeginTxWithTimeout(t.Context(), txBeginTimeoutConfig[*timeoutTestTx]{
		timeout: 50 * time.Millisecond,
		begin: func(ctx context.Context) (*timeoutTestTx, error) {
			beginCtx = ctx
			return &timeoutTestTx{}, nil
		},
		isNil: func(tx *timeoutTestTx) bool { return tx == nil },
		onTxStarted: func(_ *timeoutTestTx, cancel context.CancelFunc) {
			capturedCancel = cancel
		},
		rollback:               func(*timeoutTestTx) error { return nil },
		timeoutErr:             errors.New("timeout"),
		beginFailureErr:        errors.New("begin failed"),
		beginTimeoutWarn:       "begin timeout",
		timeoutRollbackNilWarn: "rollback nil",
		timeoutRollbackWarn:    "rollback warn",
	})
	require.NoError(t, err)
	require.NotNil(t, tx)
	require.NotNil(t, capturedCancel, "onTxStarted should have been called with a cancel func")

	// Simulate commit/rollback: cancel should immediately cancel beginCtx.
	capturedCancel()
	require.Error(t, beginCtx.Err(), "beginCtx should be cancelled after cancel func is called")
}

func TestGetOrBeginTxWithTimeout_NoTimeoutStillInvokesOnTxStarted(t *testing.T) {
	t.Parallel()

	var capturedCancel context.CancelFunc

	tx, err := getOrBeginTxWithTimeout(t.Context(), txBeginTimeoutConfig[*timeoutTestTx]{
		timeout: 0,
		begin: func(ctx context.Context) (*timeoutTestTx, error) {
			return &timeoutTestTx{}, nil
		},
		isNil: func(tx *timeoutTestTx) bool { return tx == nil },
		onTxStarted: func(_ *timeoutTestTx, cancel context.CancelFunc) {
			capturedCancel = cancel
		},
		rollback:               func(*timeoutTestTx) error { return nil },
		timeoutErr:             errors.New("timeout"),
		beginFailureErr:        errors.New("begin failed"),
		beginTimeoutWarn:       "begin timeout",
		timeoutRollbackNilWarn: "rollback nil",
		timeoutRollbackWarn:    "rollback warn",
	})
	require.NoError(t, err)
	require.NotNil(t, tx)
	require.NotNil(t, capturedCancel, "onTxStarted should still be called when timeout is disabled")

	require.NotPanics(t, func() {
		capturedCancel()
	})
}

func TestGetOrBeginTxWithTimeout_TimeoutRollbackUsesActiveBeginContext(t *testing.T) {
	t.Parallel()

	timeoutErr := errors.New("timeout")
	releaseBegin := make(chan struct{})
	rollbackSawCanceledCtx := make(chan bool, 1)

	var beginCtx context.Context
	_, err := getOrBeginTxWithTimeout(t.Context(), txBeginTimeoutConfig[*timeoutTestTx]{
		timeout: 50 * time.Millisecond,
		begin: func(ctx context.Context) (*timeoutTestTx, error) {
			beginCtx = ctx
			<-releaseBegin
			return &timeoutTestTx{}, nil
		},
		isNil: func(tx *timeoutTestTx) bool {
			return tx == nil
		},
		rollback: func(*timeoutTestTx) error {
			rollbackSawCanceledCtx <- beginCtx.Err() != nil
			return nil
		},
		timeoutErr:             timeoutErr,
		beginFailureErr:        errors.New("begin failed"),
		beginTimeoutWarn:       "begin timeout",
		timeoutRollbackNilWarn: "rollback nil",
		timeoutRollbackWarn:    "rollback warn",
	})
	require.ErrorIs(t, err, timeoutErr)

	close(releaseBegin)

	select {
	case sawCanceledCtx := <-rollbackSawCanceledCtx:
		require.False(t, sawCanceledCtx, "expected begin context to stay active until timeout rollback finishes")
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for rollback after timeout")
	}
}

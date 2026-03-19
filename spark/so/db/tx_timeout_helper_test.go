package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type timeoutTestTx struct{}

func TestGetOrBeginTxWithTimeout_ParentCancelWrapsBeginFailureErr(t *testing.T) {
	t.Parallel()

	beginFailureErr := errors.New("begin failed")
	parentCtx, parentCancel := context.WithCancel(t.Context())

	var capturedBeginCtx context.Context
	_, err := getOrBeginTxWithTimeout(parentCtx, txBeginTimeoutConfig[*timeoutTestTx]{
		timeout: 10 * time.Second,
		begin: func(ctx context.Context) (*timeoutTestTx, error) {
			capturedBeginCtx = ctx
			parentCancel() // cancel parent while begin is blocked
			<-ctx.Done()   // wait for beginCtx to be cancelled by parent propagation
			return nil, ctx.Err()
		},
		rollback:        func(*timeoutTestTx) error { return nil },
		timeoutErr:      errors.New("timeout"),
		beginFailureErr: beginFailureErr,
		txTypeName:      "test tx",
	})

	require.ErrorIs(t, err, beginFailureErr, "error should wrap beginFailureErr")
	require.ErrorIs(t, err, context.Canceled, "error should wrap context.Canceled")
	require.ErrorIs(t, capturedBeginCtx.Err(), context.Canceled, "beginCtx should be cancelled")
}

func TestGetOrBeginTxWithTimeout_TimeoutRollbackUsesActiveBeginContext(t *testing.T) {
	t.Parallel()

	timeoutErr := errors.New("timeout")
	releaseBegin := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBegin) }) }
	defer release()
	rollbackSawCanceledCtx := make(chan bool, 1)

	var beginCtx context.Context
	_, err := getOrBeginTxWithTimeout(t.Context(), txBeginTimeoutConfig[*timeoutTestTx]{
		timeout: 50 * time.Millisecond,
		begin: func(ctx context.Context) (*timeoutTestTx, error) {
			beginCtx = ctx
			select {
			case <-releaseBegin:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &timeoutTestTx{}, nil
		},
		rollback: func(*timeoutTestTx) error {
			rollbackSawCanceledCtx <- beginCtx.Err() != nil
			return nil
		},
		timeoutErr:      timeoutErr,
		beginFailureErr: errors.New("begin failed"),
		txTypeName:      "test tx",
	})
	require.ErrorIs(t, err, timeoutErr)

	release()

	select {
	case sawCanceledCtx := <-rollbackSawCanceledCtx:
		require.False(t, sawCanceledCtx, "expected begin context to stay active until timeout rollback finishes")
		require.Eventually(t, func() bool {
			return beginCtx.Err() != nil
		}, 500*time.Millisecond, 10*time.Millisecond,
			"beginCtx should be cancelled after rollback completes")
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for rollback after timeout")
	}
}

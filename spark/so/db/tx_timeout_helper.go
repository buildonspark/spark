package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"go.uber.org/zap"
)

type txBeginResult[Tx any] struct {
	tx  Tx
	err error
}

type txBeginTimeoutConfig[Tx any] struct {
	timeout time.Duration
	begin   func(context.Context) (Tx, error)
	isNil   func(Tx) bool
	// onTxStarted is called with the started transaction and the cancel func for beginCtx.
	// The callee MUST arrange for cancel to be called when the transaction ends (commit or
	// rollback); failing to do so leaks beginCtx for up to cfg.timeout after the transaction
	// completes. If nil, beginCtx is cancelled immediately on success, so the returned
	// transaction's context will already be Done — only safe if the backend does not use the
	// context beyond the Begin call.
	onTxStarted            func(Tx, context.CancelFunc)
	rollback               func(Tx) error
	timeoutErr             error
	beginFailureErr        error
	beginTimeoutWarn       string
	timeoutRollbackNilWarn string
	timeoutRollbackWarn    string
}

func getOrBeginTxWithTimeout[Tx any](ctx context.Context, cfg txBeginTimeoutConfig[Tx]) (Tx, error) {
	if cfg.timeout <= 0 {
		tx, err := cfg.begin(ctx)
		if err != nil {
			return tx, err
		}
		if !cfg.isNil(tx) && cfg.onTxStarted != nil {
			// No beginCtx is created in the no-timeout path, so this cancel is
			// intentionally a no-op; callers cannot use it to cancel tx work.
			cfg.onTxStarted(tx, func() {})
		}
		return tx, nil
	}

	var zeroTx Tx
	resultChan := make(chan txBeginResult[Tx])
	beginReturned := make(chan struct{})
	logger := logging.GetLoggerFromContext(ctx)

	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, cfg.timeout)
	defer timeoutCancel()
	beginCtx, beginCancel := context.WithCancel(ctx)

	go func() {
		defer close(resultChan)

		tx, err := cfg.begin(beginCtx)
		close(beginReturned)
		if err != nil {
			beginCancel()
			select {
			case resultChan <- txBeginResult[Tx]{tx: zeroTx, err: err}:
				return
			case <-timeoutCtx.Done():
				logger.Warn(cfg.beginTimeoutWarn, zap.Error(err))
				return
			}
		}

		select {
		case resultChan <- txBeginResult[Tx]{tx: tx, err: nil}:
		case <-timeoutCtx.Done():
			// cfg.rollback runs while beginCtx is still active so the DB connection
			// remains usable for the rollback call.
			// NOTE: if cfg.rollback hangs, beginCancel() is never called here;
			// the delayed goroutine already exited via <-beginReturned (closed above).
			// The only backstop in that case is parent ctx cancellation.
			if cfg.isNil(tx) {
				logger.Warn(cfg.timeoutRollbackNilWarn)
			} else if rollbackErr := cfg.rollback(tx); rollbackErr != nil {
				logger.Warn(cfg.timeoutRollbackWarn, zap.Error(rollbackErr))
			}
			beginCancel()
			return
		}
	}()

	select {
	case res, ok := <-resultChan:
		if ok {
			if res.err == nil {
				if cfg.isNil(res.tx) {
					// Defensive cleanup for the unreachable nil-tx/no-error case.
					beginCancel()
				} else if cfg.onTxStarted != nil {
					cfg.onTxStarted(res.tx, beginCancel)
					beginCancel = func() {}
				} else {
					beginCancel()
				}
			}
			// On error, beginCancel() was already called by the inner goroutine before
			// sending the result, so no explicit call is needed here.
			return res.tx, res.err
		}
	case <-timeoutCtx.Done():
		// Timed out before the inner goroutine sent a result. beginCancel() is NOT called here;
		// the delayed goroutine below manages it so a transaction that began just after timeout
		// can still roll back using its original begin context.
	}
	// Both select arms reach here: ok==false (inner goroutine exited; beginCancel() already called)
	// or timeoutCtx.Done() (timed out; beginCancel() is managed by the delayed goroutine below).
	// In the ok==false case the delayed goroutine also fires but exits immediately via
	// <-beginReturned (already closed); the double-call to beginCancel() is safe (idempotent).
	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		// Delay begin context cancellation so a transaction that began just after timeout can still
		// rollback using its original begin context.
		//
		// Safety net:
		// - If begin already returned, the begin goroutine owns cleanup and this goroutine exits.
		// - If begin is still running, we eventually call beginCancel after another timeout (or parent
		//   context cancellation) so the begin context does not live forever.
		go func() {
			select {
			case <-beginReturned:
			case <-time.After(cfg.timeout):
				beginCancel()
			case <-ctx.Done():
				beginCancel()
			}
		}()
		return zeroTx, cfg.timeoutErr
	}

	if cfg.beginFailureErr == nil {
		return zeroTx, timeoutCtx.Err()
	}
	return zeroTx, fmt.Errorf("%w: %w", cfg.beginFailureErr, timeoutCtx.Err())
}

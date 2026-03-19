package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
)

type txBeginResult[Tx any] struct {
	tx  Tx
	err error
}

type txBeginTimeoutConfig[Tx comparable] struct {
	timeout    time.Duration
	txTypeName string
	begin      func(context.Context) (Tx, error)
	// rollback is called to clean up a transaction that began after the timeout fired.
	// It runs while beginCtx is still active so the connection remains usable.
	rollback        func(Tx) error
	timeoutErr      error
	beginFailureErr error
}

func getOrBeginTxWithTimeout[Tx comparable](ctx context.Context, cfg txBeginTimeoutConfig[Tx]) (Tx, error) {
	if cfg.timeout <= 0 {
		return cfg.begin(ctx)
	}

	var zeroTx Tx
	resultChan := make(chan txBeginResult[Tx])
	beginReturned := make(chan struct{})
	logger := logging.GetLoggerFromContext(ctx)

	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, cfg.timeout)
	defer timeoutCancel()
	// beginCtx is a child of ctx (the session context), not of timeoutCtx, so it can
	// be cancelled independently of the begin timeout. On error and timeout paths,
	// beginCancel is called explicitly. On the success path, beginCancel is intentionally
	// not called here: pgx/stdlib stores the context passed to BeginTx in wrapTx.ctx
	// and uses it for every subsequent operation on the connection, so cancelling it
	// early would break those subsequent queries. Instead, beginCtx is cancelled
	// automatically when the session ends via parent context propagation.
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
				logger.Sugar().Warnf("failed to begin %s within timeout: %v", cfg.txTypeName, err)
				return
			}
		}

		select {
		case resultChan <- txBeginResult[Tx]{tx: tx, err: nil}:
		case <-timeoutCtx.Done():
			// cfg.rollback runs while beginCtx is still active so the DB connection
			// remains usable for the rollback call. A second timeout backstop bounds
			// how long a hung rollback can hold beginCtx open.
			if tx == zeroTx {
				logger.Sugar().Warnf("wanted to rollback %s after timeout, but tx is nil", cfg.txTypeName)
			} else {
				rollbackDone := make(chan struct{})
				backstop := time.NewTimer(cfg.timeout)
				defer backstop.Stop()
				go func() {
					defer close(rollbackDone)
					if rollbackErr := cfg.rollback(tx); rollbackErr != nil {
						logger.Sugar().Warnf("failed to rollback %s after timeout: %v", cfg.txTypeName, rollbackErr)
					}
				}()
				select {
				case <-rollbackDone:
				case <-backstop.C:
					logger.Sugar().Warnf("failed to rollback %s after timeout: rollback did not complete in time", cfg.txTypeName)
				}
			}
			beginCancel()
			return
		}
	}()

	select {
	case res, ok := <-resultChan:
		if ok {
			if res.err == nil && res.tx == zeroTx {
				// Defensive cleanup for the unreachable nil-tx/no-error case.
				beginCancel()
				return zeroTx, fmt.Errorf("begin returned nil tx with no error")
			}
			// On error, beginCancel() was already called by the inner goroutine before
			// sending the result, so no explicit call is needed here.
			// On success, beginCtx is left open; see comment above.
			if res.err != nil && cfg.beginFailureErr != nil {
				return zeroTx, fmt.Errorf("%w: %w", cfg.beginFailureErr, res.err)
			}
			return res.tx, res.err
		}
	case <-timeoutCtx.Done():
		// Timed out before the inner goroutine sent a result. beginCancel() is NOT called here;
		// the delayed goroutine below manages it so a transaction that began just after timeout
		// can still roll back using its original begin context.
	}
	// Both select arms reach here:
	// - ok==false: inner goroutine exited without sending. Two sub-cases:
	//     a) error path: begin returned an error; beginCancel() was called before the send
	//        attempt, and timeoutCtx.Done() won the send race.
	//     b) rollback path: begin returned a valid tx but the outer select had already timed
	//        out; the inner goroutine rolled back, called beginCancel(), and exited —
	//        defer close(resultChan) may then lose a race with the outer select.
	//   In both sub-cases beginCancel() is already called; the safety-net exits immediately
	//   via <-beginReturned.
	// - timeoutCtx.Done(): timed out before the inner goroutine sent a result; beginCancel()
	//   is managed entirely by the safety-net goroutine below.
	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		// Delay begin context cancellation so a transaction that began just after timeout can still
		// rollback using its original begin context.
		//
		// Safety net:
		// - If begin already returned, the begin goroutine owns cleanup; skip the goroutine.
		// - If begin is still running, we eventually call beginCancel after another timeout (or parent
		//   context cancellation) so the begin context does not live forever.
		select {
		case <-beginReturned:
			// begin already returned; the inner goroutine called beginCancel().
		default:
			go func() {
				safetyTimer := time.NewTimer(cfg.timeout)
				defer safetyTimer.Stop()
				select {
				case <-beginReturned:
				case <-safetyTimer.C:
					beginCancel()
				case <-ctx.Done():
					beginCancel()
				}
			}()
		}
		return zeroTx, cfg.timeoutErr
	}

	if cfg.beginFailureErr == nil {
		return zeroTx, timeoutCtx.Err()
	}
	return zeroTx, fmt.Errorf("%w: %w", cfg.beginFailureErr, timeoutCtx.Err())
}

// Package wallet moves money in and out of customer wallets.
//
// A wallet is a liability ledger account plus the rules of its wallet type.
// Every operation here is a journal through the posting engine; nothing
// writes a balance directly, and nothing outside this package decides whether
// a wallet operation is allowed.
package wallet

import (
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// Operation is what is being done to a wallet, as far as its wallet type's
// rules are concerned.
type Operation string

const (
	// OpFund loads money the customer paid for (a top-up).
	OpFund Operation = "fund"
	// OpGrant credits promotional value nobody paid for.
	OpGrant Operation = "grant"
	// OpSpend takes value out to pay for something.
	OpSpend Operation = "spend"
	// OpHold reserves value for a spend not yet final.
	OpHold Operation = "hold"
	// OpTransferOut and OpTransferIn are the two sides of a customer-to-customer transfer.
	OpTransferOut Operation = "transfer_out"
	OpTransferIn  Operation = "transfer_in"
	// OpWithdraw cashes value out to a bank account.
	OpWithdraw Operation = "withdraw"
	// OpRefundIn returns value from a refunded purchase.
	OpRefundIn Operation = "refund_in"
	// OpAdjust is an operator correction. It bypasses capability and limit
	// rules on purpose — it is how a wallet those rules got wrong is put
	// right — and is instead gated by permission, reason code and audit.
	OpAdjust Operation = "adjust"
)

// credits reports whether op increases the wallet's balance.
func (op Operation) credits() bool {
	switch op {
	case OpFund, OpGrant, OpTransferIn, OpRefundIn:
		return true
	}
	return false
}

// loads reports whether op counts towards the daily load limit: new value
// arriving, as opposed to the customer's own value coming back.
func (op Operation) loads() bool {
	switch op {
	case OpFund, OpGrant, OpTransferIn:
		return true
	}
	return false
}

// assertAllowed is the single place a wallet type's capabilities are enforced
// (plan.md D10). Every wallet operation calls it before touching the ledger.
//
// Scattered "if walletType.Fundable" checks are how rules like these rot: one
// code path forgets, and a BONUS wallet becomes withdrawable. Keeping them in
// one switch makes the whole policy readable at once.
//
// It checks what can be known before posting. Limits that depend on the
// balance being moved — maximum balance, daily load — are checked by
// checkLimitsAfterPosting, under the ledger lock.
func assertAllowed(walletType *dao.WalletType, wallet *dao.Wallet, op Operation, amount int64) error {
	if amount <= 0 {
		return apperrors.New(apperrors.InvalidArgument, "amount must be positive, got %d", amount)
	}
	if op == OpAdjust {
		return nil
	}
	if wallet.Status != dao.WalletActive {
		return denied("wallet %s is %s", wallet.PublicID, wallet.Status)
	}

	var capable bool
	switch op {
	case OpFund:
		capable = walletType.Fundable
	case OpGrant:
		capable = walletType.Grantable
	case OpWithdraw:
		capable = walletType.Withdrawable
	case OpTransferOut, OpTransferIn:
		capable = walletType.Transferable
	case OpRefundIn:
		capable = walletType.RefundableToSource
	case OpSpend, OpHold:
		capable = true
	default:
		return apperrors.New(apperrors.Internal, "unknown wallet operation %q", op)
	}
	if !capable {
		return denied("wallet type %s does not allow %s", walletType.Code, op)
	}

	if walletType.MaxTxnAmount != nil && amount > *walletType.MaxTxnAmount {
		return denied("%d exceeds the %s wallet's per-transaction limit of %d",
			amount, walletType.Code, *walletType.MaxTxnAmount)
	}
	return nil
}

func denied(format string, args ...any) error {
	return apperrors.New(apperrors.WalletOperationDenied, format, args...)
}

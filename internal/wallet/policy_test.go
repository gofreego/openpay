package wallet

import (
	"testing"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

var (
	mainType  = &dao.WalletType{Code: "MAIN", Fundable: true, RefundableToSource: true}
	bonusType = &dao.WalletType{Code: "BONUS", Grantable: true, RefundableToSource: true}
	active    = &dao.Wallet{PublicID: "wlt_1", Status: dao.WalletActive}
)

// The capability matrix for the two default types. A BONUS wallet cannot be
// withdrawn or funded; MAIN cannot be granted to. These are the rules that
// keep "how much real customer money do we hold?" answerable (plan.md D10).
func TestAssertAllowedMatrix(t *testing.T) {
	cases := []struct {
		walletType *dao.WalletType
		op         Operation
		allowed    bool
	}{
		{mainType, OpFund, true},
		{mainType, OpGrant, false},
		{mainType, OpSpend, true},
		{mainType, OpHold, true},
		{mainType, OpWithdraw, false},
		{mainType, OpTransferOut, false},
		{mainType, OpRefundIn, true},
		{bonusType, OpFund, false},
		{bonusType, OpGrant, true},
		{bonusType, OpSpend, true},
		{bonusType, OpWithdraw, false},
		{bonusType, OpTransferIn, false},
	}
	for _, tc := range cases {
		t.Run(tc.walletType.Code+"/"+string(tc.op), func(t *testing.T) {
			err := assertAllowed(tc.walletType, active, tc.op, 100)
			if tc.allowed && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !tc.allowed && !apperrors.Is(err, apperrors.WalletOperationDenied) {
				t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.WalletOperationDenied)
			}
		})
	}
}

func TestAssertAllowedLimitsAndStatus(t *testing.T) {
	limit := int64(500)
	limited := &dao.WalletType{Code: "MAIN", Fundable: true, MaxTxnAmount: &limit}

	if err := assertAllowed(limited, active, OpFund, 500); err != nil {
		t.Errorf("amount at the limit refused: %v", err)
	}
	if err := assertAllowed(limited, active, OpFund, 501); !apperrors.Is(err, apperrors.WalletOperationDenied) {
		t.Errorf("amount over the per-transaction limit: error code = %q", apperrors.CodeOf(err))
	}
	if err := assertAllowed(limited, active, OpFund, 0); !apperrors.Is(err, apperrors.InvalidArgument) {
		t.Errorf("zero amount: error code = %q", apperrors.CodeOf(err))
	}

	frozen := &dao.Wallet{PublicID: "wlt_2", Status: dao.WalletFrozen}
	if err := assertAllowed(mainType, frozen, OpSpend, 100); !apperrors.Is(err, apperrors.WalletOperationDenied) {
		t.Errorf("spend from a frozen wallet: error code = %q", apperrors.CodeOf(err))
	}
	// Adjustments are how a frozen or misconfigured wallet is put right, so
	// no capability or limit stands in their way.
	if err := assertAllowed(limited, frozen, OpAdjust, 10_000); err != nil {
		t.Errorf("adjustment refused: %v", err)
	}
}

func TestWithdrawalPolicy(t *testing.T) {
	min, threshold := int64(10000), int64(500000)
	cashable := &dao.WalletType{Code: "CASH", Fundable: true, Withdrawable: true,
		MinWithdrawalAmount: &min, WithdrawalApprovalThreshold: &threshold}

	if err := assertAllowed(cashable, active, OpWithdraw, 9999); !apperrors.Is(err, apperrors.WalletOperationDenied) {
		t.Errorf("below the minimum: error code = %q", apperrors.CodeOf(err))
	}
	if err := assertAllowed(cashable, active, OpWithdraw, 10000); err != nil {
		t.Errorf("exactly the minimum refused: %v", err)
	}
	if NeedsApproval(cashable, 500000) {
		t.Error("a withdrawal at the threshold should go out automatically")
	}
	if !NeedsApproval(cashable, 500001) {
		t.Error("a withdrawal above the threshold should wait for approval")
	}
	if NeedsApproval(mainType, 1_000_000_00) {
		t.Error("a type with no threshold needs no approval")
	}
	if err := assertAllowed(mainType, active, OpWithdraw, 10000); !apperrors.Is(err, apperrors.WalletOperationDenied) {
		t.Error("a non-withdrawable type allowed a withdrawal")
	}
}

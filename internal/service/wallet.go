package service

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// The wallet boundary is where a platform-wide customer meets per-product
// balances, so it is where D8 is strictest: a product backend sees its own
// product's wallets and platform-scoped ones, never another product's; an
// operator sees what their scope grants, and platform wallets only with the
// whole-estate scope.

// walletViewer resolves who may see which wallets.
type walletViewer struct {
	scope *filter.ProductScope
	// includePlatform: platform-scoped wallets are spendable in every
	// product, so every product backend may see them — but they belong to no
	// product, so a product operator may not.
	includePlatform bool
	// serviceProductID is the calling product backend's product, or 0.
	serviceProductID int64
}

func (v walletViewer) sees(w *dao.Wallet) bool {
	if w.ProductID == nil {
		return v.includePlatform
	}
	return v.scope.AllowsProduct(*w.ProductID)
}

// walletViewerFor authorises the caller for wallet access: a product backend
// always, an operator only with permission.
func (s *Service) walletViewerFor(ctx context.Context, operatorPermission string) (walletViewer, error) {
	caller, ok := appcontext.CallerFrom(ctx)
	if ok && caller.IsService() {
		return walletViewer{
			scope:            filter.OnlyProducts(caller.ProductID),
			includePlatform:  true,
			serviceProductID: caller.ProductID,
		}, nil
	}
	if err := auth.RequireOperator(ctx, operatorPermission); err != nil {
		return walletViewer{}, err
	}
	scope, err := s.callerScope(ctx)
	if err != nil {
		return walletViewer{}, err
	}
	return walletViewer{scope: scope, includePlatform: scope.All()}, nil
}

// visibleWallet loads a wallet the viewer may see. Anything else is NotFound,
// so a product cannot probe for another product's wallet ids.
func (s *Service) visibleWallet(ctx context.Context, viewer walletViewer, id string) (*dao.Wallet, error) {
	w, err := s.repo.GetWalletByPublicID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !viewer.sees(w) {
		return nil, apperrors.New(apperrors.NotFound, "wallet %q not found", id)
	}
	return w, nil
}

func (s *Service) OpenWallet(ctx context.Context, req *openpay_v1.OpenWalletRequest) (*openpay_v1.OpenWalletResponse, error) {
	productID, err := auth.RequireService(ctx)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	customer, err := s.repo.GetCustomerByPublicID(ctx, req.GetCustomerId())
	if err != nil {
		return nil, err
	}
	walletType, err := s.walletTypeByCode(ctx, productID, req.GetWalletTypeCode())
	if err != nil {
		return nil, err
	}

	// Naturally idempotent on (customer, type), like UpsertCustomer.
	var (
		w       *dao.Wallet
		created bool
	)
	err = s.repo.WithTx(ctx, func(ctx context.Context) error {
		w, created, err = s.wallets.Open(ctx, customer, walletType)
		if err != nil || !created {
			return err
		}
		return s.audit(ctx, auditParams{
			Action: "wallet.opened", ResourceType: "wallet", ResourceID: w.PublicID,
			ProductID: w.ProductID, After: w,
		})
	})
	if err != nil {
		return nil, err
	}

	out, err := s.toProtoWallet(ctx, w)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.OpenWalletResponse{Wallet: out, Created: created}, nil
}

// walletTypeByCode finds a type a product may open: its own, or a platform
// type. The product's own type wins if both share a code.
func (s *Service) walletTypeByCode(ctx context.Context, productID int64, code string) (*dao.WalletType, error) {
	types, err := s.repo.ListWalletTypes(ctx, productID)
	if err != nil {
		return nil, err
	}
	var platform *dao.WalletType
	for _, wt := range types {
		if wt.Code != code {
			continue
		}
		if wt.ProductID != nil {
			return wt, nil
		}
		platform = wt
	}
	if platform != nil {
		return platform, nil
	}
	return nil, apperrors.New(apperrors.NotFound, "wallet type %q not found for this product", code)
}

func (s *Service) GetWallet(ctx context.Context, req *openpay_v1.GetWalletRequest) (*openpay_v1.GetWalletResponse, error) {
	viewer, err := s.walletViewerFor(ctx, auth.PermWalletsRead)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	w, err := s.visibleWallet(ctx, viewer, req.GetId())
	if err != nil {
		return nil, err
	}
	out, err := s.toProtoWallet(ctx, w)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.GetWalletResponse{Wallet: out}, nil
}

func (s *Service) ListCustomerWallets(ctx context.Context, req *openpay_v1.ListCustomerWalletsRequest) (*openpay_v1.ListCustomerWalletsResponse, error) {
	viewer, err := s.walletViewerFor(ctx, auth.PermWalletsRead)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	customer, err := s.repo.GetCustomerByPublicID(ctx, req.GetCustomerId())
	if err != nil {
		return nil, err
	}

	wallets, err := s.repo.ListCustomerWallets(ctx, customer.ID, viewer.scope, viewer.includePlatform)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListCustomerWalletsResponse{}
	for _, w := range wallets {
		// The query already scoped; this is the second lock on the same door.
		if !viewer.sees(w) {
			return nil, apperrors.New(apperrors.Internal, "wallet listing returned a wallet outside the caller's scope")
		}
		out, err := s.toProtoWallet(ctx, w)
		if err != nil {
			return nil, err
		}
		response.Wallets = append(response.Wallets, out)
	}
	return response, nil
}

func (s *Service) GetWalletStatement(ctx context.Context, req *openpay_v1.GetWalletStatementRequest) (*openpay_v1.GetWalletStatementResponse, error) {
	viewer, err := s.walletViewerFor(ctx, auth.PermWalletsRead)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	w, err := s.visibleWallet(ctx, viewer, req.GetWalletId())
	if err != nil {
		return nil, err
	}

	entries, next, err := s.statementPage(ctx, w.LedgerAccountID, dao.AccountLiability, req.GetLimit(), req.GetCursor())
	if err != nil {
		return nil, err
	}
	out, err := s.toProtoWallet(ctx, w)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.GetWalletStatementResponse{Wallet: out, Entries: entries, NextCursor: next}, nil
}

func (s *Service) GrantWallet(ctx context.Context, req *openpay_v1.GrantWalletRequest) (*openpay_v1.GrantWalletResponse, error) {
	viewer, err := s.walletViewerFor(ctx, auth.PermWalletsGrant)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	return idempotent(ctx, s.repo, "GrantWallet", req,
		func(ctx context.Context) (*openpay_v1.GrantWalletResponse, error) {
			w, err := s.visibleWallet(ctx, viewer, req.GetWalletId())
			if err != nil {
				return nil, err
			}
			funding, err := s.fundingProduct(ctx, viewer, w, req.GetFundingProductId())
			if err != nil {
				return nil, err
			}

			journal, err := s.wallets.Grant(ctx, wallet.GrantRequest{
				Wallet: w, Amount: req.GetAmount(), FundingProductID: funding,
				ReasonCode: req.GetReasonCode(), Memo: req.GetMemo(),
				Reference: appcontext.IdempotencyKey(ctx),
			})
			if err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action: "wallet.granted", ResourceType: "wallet", ResourceID: w.PublicID,
				ProductID: &funding,
				After: map[string]any{"amount": req.GetAmount(), "reason_code": req.GetReasonCode(),
					"journal_id": journal.PublicID},
			}); err != nil {
				return nil, err
			}

			out, err := s.toProtoWallet(ctx, w)
			if err != nil {
				return nil, err
			}
			return &openpay_v1.GrantWalletResponse{Wallet: out, JournalId: journal.PublicID}, nil
		})
}

// fundingProduct decides whose budget pays for a grant or adjustment.
//
// A product wallet is funded by its own product. A platform wallet is funded
// by the calling product backend, or by the product an operator names —
// which must be in their scope, or an operator could charge a product they
// do not look after.
func (s *Service) fundingProduct(ctx context.Context, viewer walletViewer, w *dao.Wallet, named string) (int64, error) {
	if w.ProductID != nil {
		return *w.ProductID, nil
	}
	if viewer.serviceProductID != 0 {
		return viewer.serviceProductID, nil
	}
	if named == "" {
		return 0, apperrors.New(apperrors.InvalidArgument,
			"a platform-scoped wallet belongs to no product: name the product that funds this")
	}
	product, err := s.repo.GetProductByPublicID(ctx, named)
	if err != nil {
		return 0, err
	}
	if err := requireProductInScope(viewer.scope, product.ID); err != nil {
		return 0, err
	}
	return product.ID, nil
}

func (s *Service) TransferWallet(ctx context.Context, req *openpay_v1.TransferWalletRequest) (*openpay_v1.TransferWalletResponse, error) {
	if _, err := auth.RequireService(ctx); err != nil {
		return nil, err
	}
	viewer, err := s.walletViewerFor(ctx, "")
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	return idempotent(ctx, s.repo, "TransferWallet", req,
		func(ctx context.Context) (*openpay_v1.TransferWalletResponse, error) {
			from, err := s.visibleWallet(ctx, viewer, req.GetFromWalletId())
			if err != nil {
				return nil, err
			}
			recipient, err := s.repo.GetCustomerByPublicID(ctx, req.GetToCustomerId())
			if err != nil {
				return nil, err
			}
			walletType, err := s.repo.GetWalletTypeByID(ctx, from.WalletTypeID)
			if err != nil {
				return nil, err
			}
			to, _, err := s.wallets.Open(ctx, recipient, walletType)
			if err != nil {
				return nil, err
			}

			journal, err := s.wallets.Transfer(ctx, wallet.TransferRequest{
				From: from, To: to, Amount: req.GetAmount(), Reference: appcontext.IdempotencyKey(ctx),
			})
			if err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action: "wallet.transferred", ResourceType: "wallet", ResourceID: from.PublicID,
				ProductID: from.ProductID,
				After: map[string]any{"to_wallet_id": to.PublicID, "amount": req.GetAmount(),
					"journal_id": journal.PublicID},
			}); err != nil {
				return nil, err
			}

			fromOut, err := s.toProtoWallet(ctx, from)
			if err != nil {
				return nil, err
			}
			toOut, err := s.toProtoWallet(ctx, to)
			if err != nil {
				return nil, err
			}
			return &openpay_v1.TransferWalletResponse{From: fromOut, To: toOut, JournalId: journal.PublicID}, nil
		})
}

func (s *Service) AdjustWallet(ctx context.Context, req *openpay_v1.AdjustWalletRequest) (*openpay_v1.AdjustWalletResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermWalletsAdjust); err != nil {
		return nil, err
	}
	viewer, err := s.walletViewerFor(ctx, auth.PermWalletsAdjust)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	return idempotent(ctx, s.repo, "AdjustWallet", req,
		func(ctx context.Context) (*openpay_v1.AdjustWalletResponse, error) {
			w, err := s.visibleWallet(ctx, viewer, req.GetWalletId())
			if err != nil {
				return nil, err
			}
			funding, err := s.fundingProduct(ctx, viewer, w, req.GetProductId())
			if err != nil {
				return nil, err
			}
			before, err := s.repo.GetBalance(ctx, w.LedgerAccountID)
			if err != nil {
				return nil, err
			}

			direction := dao.Credit
			if req.GetDirection() == openpay_v1.PostingDirection_POSTING_DIRECTION_DEBIT {
				direction = dao.Debit
			}
			journal, err := s.wallets.Adjust(ctx, wallet.AdjustRequest{
				Wallet: w, Amount: req.GetAmount(), Direction: direction, ProductID: funding,
				ReasonCode: req.GetReasonCode(), Memo: req.GetMemo(),
				Reference: appcontext.IdempotencyKey(ctx),
			})
			if err != nil {
				return nil, err
			}
			after, err := s.repo.GetBalance(ctx, w.LedgerAccountID)
			if err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action: "wallet.adjusted", ResourceType: "wallet", ResourceID: w.PublicID,
				ProductID: &funding,
				Before:    map[string]any{"balance": before.Natural(dao.AccountLiability)},
				After: map[string]any{"balance": after.Natural(dao.AccountLiability),
					"reason_code": req.GetReasonCode(), "memo": req.GetMemo(), "journal_id": journal.PublicID},
			}); err != nil {
				return nil, err
			}

			out, err := s.toProtoWallet(ctx, w)
			if err != nil {
				return nil, err
			}
			return &openpay_v1.AdjustWalletResponse{Wallet: out, JournalId: journal.PublicID}, nil
		})
}

func (s *Service) GetFloatHeld(ctx context.Context, req *openpay_v1.GetFloatHeldRequest) (*openpay_v1.GetFloatHeldResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermWalletsRead); err != nil {
		return nil, err
	}
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}

	lines, err := s.repo.FloatHeld(ctx, scope)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.GetFloatHeldResponse{AsOf: timestamppb.New(time.Now())}
	for _, l := range lines {
		response.Lines = append(response.Lines, &openpay_v1.FloatHeldLine{
			ProductId: deref(l.ProductPublicID),
			Currency:  l.Currency,
			Amount:    l.Amount,
			Wallets:   l.Wallets,
		})
	}
	return response, nil
}

func (s *Service) toProtoWallet(ctx context.Context, w *dao.Wallet) (*openpay_v1.Wallet, error) {
	walletType, err := s.repo.GetWalletTypeByID(ctx, w.WalletTypeID)
	if err != nil {
		return nil, err
	}
	balance, err := s.repo.GetBalance(ctx, w.LedgerAccountID)
	if err != nil {
		return nil, err
	}
	status := openpay_v1.WalletStatus_WALLET_STATUS_ACTIVE
	if w.Status == dao.WalletFrozen {
		status = openpay_v1.WalletStatus_WALLET_STATUS_FROZEN
	}
	return &openpay_v1.Wallet{
		Id:             w.PublicID,
		CustomerId:     w.CustomerPublicID,
		WalletTypeId:   walletType.PublicID,
		WalletTypeCode: walletType.Code,
		ProductId:      deref(walletType.ProductPublicID),
		Currency:       walletType.Currency,
		Status:         status,
		Balance: &openpay_v1.LedgerBalance{
			Balance:   balance.Natural(dao.AccountLiability),
			Held:      balance.Held,
			Available: balance.Available(dao.AccountLiability),
		},
		CreatedAt: timestamppb.New(w.CreatedAt),
	}, nil
}

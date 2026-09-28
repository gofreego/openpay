package service

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/internal/recon"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// Reconciliation is platform-level throughout: a settlement mixes every
// product's money, so only the whole-estate scope works it (U-D6).

func (s *Service) ListSettlements(ctx context.Context, req *openpay_v1.ListSettlementsRequest) (*openpay_v1.ListSettlementsResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermReconRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	settlements, err := s.repo.ListSettlements(ctx, req.GetProvider(), limit)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListSettlementsResponse{}
	for _, st := range settlements {
		response.Settlements = append(response.Settlements, toProtoSettlement(st))
	}
	return response, nil
}

func (s *Service) GetSettlement(ctx context.Context, req *openpay_v1.GetSettlementRequest) (*openpay_v1.GetSettlementResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermReconRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	st, err := s.repo.GetSettlementByPublicID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	out := toProtoSettlement(st)

	items, err := s.repo.ListSettlementItems(ctx, st.ID)
	if err != nil {
		return nil, err
	}
	productIDs := map[int64]string{}
	shares, err := s.repo.SettlementByProduct(ctx, st.ID)
	if err != nil {
		return nil, err
	}
	for _, sh := range shares {
		share := &openpay_v1.SettlementProductShare{Lines: sh.Lines, Gross: sh.Gross, Fees: sh.Fees, FeeTax: sh.FeeTax, Net: sh.Net}
		if sh.ProductID != nil {
			share.ProductId = *sh.ProductPublicID
			productIDs[*sh.ProductID] = *sh.ProductPublicID
		}
		out.ByProduct = append(out.ByProduct, share)
	}
	for _, it := range items {
		line := &openpay_v1.SettlementLine{Kind: it.Kind, ProviderRef: it.ProviderRef, Gross: it.Gross, Fee: it.Fee,
			FeeTax: it.FeeTax, Net: it.Net, Expected: it.Expected, Unexplained: it.Unexplained,
			Classification: string(it.Classification)}
		if it.ProductID != nil {
			line.ProductId = productIDs[*it.ProductID]
		}
		out.Lines = append(out.Lines, line)
	}
	return &openpay_v1.GetSettlementResponse{Settlement: out}, nil
}

func (s *Service) ListReconBreaks(ctx context.Context, req *openpay_v1.ListReconBreaksRequest) (*openpay_v1.ListReconBreaksResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermReconRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 50
	}
	breaks, err := s.repo.ListBreaks(ctx, filter.AllProducts(), dao.BreakStatus(req.GetStatus()), limit)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListReconBreaksResponse{}
	for _, b := range breaks {
		out, err := s.toProtoBreak(ctx, b)
		if err != nil {
			return nil, err
		}
		response.Breaks = append(response.Breaks, out)
	}
	return response, nil
}

func (s *Service) ResolveReconBreak(ctx context.Context, req *openpay_v1.ResolveReconBreakRequest) (*openpay_v1.ResolveReconBreakResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermReconManage); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	caller, _ := appcontext.CallerFrom(ctx)
	resolution := recon.Resolution{BreakID: req.GetId(), ReasonCode: req.GetReasonCode(), Note: req.GetNote(),
		By: caller.UserID, PaymentID: req.GetPaymentId()}

	// No idempotency key: a break can be resolved once; a retry after success
	// is refused rather than applied twice.
	var b *dao.ReconBreak
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		switch req.GetAction() {
		case openpay_v1.BreakAction_BREAK_ACTION_RESOLVE:
			b, err = s.recon.Resolve(ctx, resolution)
		case openpay_v1.BreakAction_BREAK_ACTION_FORCE_MATCH:
			b, err = s.recon.ForceMatch(ctx, resolution)
		case openpay_v1.BreakAction_BREAK_ACTION_WRITE_OFF:
			b, err = s.recon.WriteOff(ctx, resolution)
		default:
			err = apperrors.New(apperrors.InvalidArgument, "unknown action")
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, auditParams{
			Action: "recon_break." + string(b.Status), ResourceType: "recon_break", ResourceID: b.PublicID,
			ProductID: b.ProductID,
			After: map[string]any{"classification": b.Classification, "amount": b.Amount,
				"reason_code": req.GetReasonCode(), "note": req.GetNote(), "payment_id": req.GetPaymentId()},
		})
	})
	if err != nil {
		return nil, err
	}
	out, err := s.toProtoBreak(ctx, b)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.ResolveReconBreakResponse{Break: out}, nil
}

func (s *Service) GetReconSummary(ctx context.Context, req *openpay_v1.GetReconSummaryRequest) (*openpay_v1.GetReconSummaryResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermReconRead); err != nil {
		return nil, err
	}
	alerts := s.alerts
	alerts.WithDefaults()
	summary, err := s.recon.Summarize(ctx, s.repo, alerts.AgedAfter)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.GetReconSummaryResponse{Summary: toProtoSummary(summary)}, nil
}

func (s *Service) RunReconCycle(ctx context.Context, req *openpay_v1.RunReconCycleRequest) (*openpay_v1.RunReconCycleResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermReconManage); err != nil {
		return nil, err
	}
	summary, err := s.recon.Cycle(ctx, s.repo, s.alerts)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.RunReconCycleResponse{Summary: toProtoSummary(summary)}, nil
}

func (s *Service) GetFeeVariance(ctx context.Context, req *openpay_v1.GetFeeVarianceRequest) (*openpay_v1.GetFeeVarianceResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermReconRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	lines, err := s.repo.FeeVariance(ctx, req.GetFrom().AsTime(), req.GetTo().AsTime())
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.GetFeeVarianceResponse{}
	for _, l := range lines {
		response.Lines = append(response.Lines, &openpay_v1.FeeVarianceLine{ProductId: l.ProductPublicID,
			Charged: l.Charged, Cost: l.Cost, Variance: l.Charged - l.Cost})
	}
	return response, nil
}

func toProtoSettlement(st *dao.Settlement) *openpay_v1.Settlement {
	return &openpay_v1.Settlement{
		Id: st.PublicID, Provider: st.Provider, ProviderSettlementId: st.ProviderSettlementID, Bank: st.Bank,
		BankReference: st.BankReference, SettledAt: timestamppb.New(st.SettledAt),
		Gross: st.Gross, Fees: st.Fees, FeeTax: st.FeeTax, Net: st.Net, Currency: st.Currency,
		ItemCount: int32(st.ItemCount), Status: string(st.Status),
	}
}

func (s *Service) toProtoBreak(ctx context.Context, b *dao.ReconBreak) (*openpay_v1.ReconBreak, error) {
	out := &openpay_v1.ReconBreak{
		Id: b.PublicID, Provider: b.Provider, Classification: string(b.Classification), Amount: b.Amount,
		Currency: b.Currency, Detail: b.Detail, Status: string(b.Status), ReasonCode: deref(b.ReasonCode),
		Note: deref(b.Note), ResolvedBy: deref(b.ResolvedBy), CreatedAt: timestamppb.New(b.CreatedAt),
	}
	if b.ResolvedAt != nil {
		out.ResolvedAt = timestamppb.New(*b.ResolvedAt)
	}
	if b.PaymentID != nil {
		p, err := s.repo.GetPaymentByID(ctx, *b.PaymentID)
		if err != nil {
			return nil, err
		}
		out.PaymentId = p.PublicID
		out.ProductId = p.ProductPublicID
	} else if b.ProductID != nil {
		product, err := s.repo.GetProductByID(ctx, *b.ProductID)
		if err != nil {
			return nil, err
		}
		out.ProductId = product.PublicID
	}
	return out, nil
}

func toProtoSummary(s *recon.Summary) *openpay_v1.ReconSummary {
	return &openpay_v1.ReconSummary{OpenBreaks: s.OpenBreaks, AgedBreaks: s.AgedBreaks,
		Suspense: s.Suspense, Receivable: s.Receivable}
}

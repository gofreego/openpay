package service

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/internal/order"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

func (s *Service) CreateItem(ctx context.Context, req *openpay_v1.CreateItemRequest) (*openpay_v1.CreateItemResponse, error) {
	productID, err := auth.RequireService(ctx)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	// Naturally idempotent: the code is unique per product.
	item := &dao.Item{
		PublicID: ids.New(ids.Item), ProductID: productID, Code: req.GetCode(), Name: req.GetName(),
		ReferencePrice: req.GetReferencePrice(), Currency: req.GetCurrency(), TaxClass: req.GetTaxClass(),
		Status: "active",
	}
	if err := s.repo.CreateItem(ctx, item); err != nil {
		return nil, err
	}
	return &openpay_v1.CreateItemResponse{Item: toProtoItem(item)}, nil
}

func (s *Service) ListItems(ctx context.Context, req *openpay_v1.ListItemsRequest) (*openpay_v1.ListItemsResponse, error) {
	productID, err := auth.RequireService(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.repo.ListItems(ctx, productID)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListItemsResponse{}
	for _, it := range items {
		response.Items = append(response.Items, toProtoItem(it))
	}
	return response, nil
}

func (s *Service) CreateOrder(ctx context.Context, req *openpay_v1.CreateOrderRequest) (*openpay_v1.CreateOrderResponse, error) {
	productID, err := auth.RequireService(ctx)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	viewer, err := s.walletViewerFor(ctx, "")
	if err != nil {
		return nil, err
	}

	return idempotent(ctx, s.repo, "CreateOrder", req,
		func(ctx context.Context) (*openpay_v1.CreateOrderResponse, error) {
			product, err := s.repo.GetProductByID(ctx, productID)
			if err != nil {
				return nil, err
			}
			create := order.CreateRequest{
				ProductID: productID, ExternalRef: req.GetExternalRef(), InvoiceRef: req.GetInvoiceRef(),
				Currency: req.GetCurrency(), Total: req.GetTotal(),
				BreakdownProvided: req.GetTaxBreakdownProvided(), AutoTender: req.GetAutoTender(),
				ReturnURL: req.GetReturnUrl(), Description: product.Name,
			}
			if req.GetDescription() != "" {
				create.Description += " — " + req.GetDescription()
			}
			if req.GetTaxBreakdownProvided() {
				create.Subtotal, create.Discount, create.Tax, create.TaxRate =
					req.GetSubtotal(), req.GetDiscount(), req.GetTax(), req.GetTaxRate()
			} else {
				// A bare total: booked gross, and the order says so (D13).
				if req.GetDiscount() != 0 || req.GetTax() != 0 {
					return nil, apperrors.New(apperrors.InvalidArgument,
						"discount and tax require tax_breakdown_provided; without it send only the total")
				}
				create.Subtotal = req.GetTotal()
			}
			for _, l := range req.GetLines() {
				create.Lines = append(create.Lines, order.Line{ItemPublicID: l.GetItemId(), Description: l.GetDescription(),
					Quantity: int(l.GetQuantity()), UnitAmount: l.GetUnitAmount()})
			}

			if req.GetCustomerId() != "" {
				customer, err := s.repo.GetCustomerByPublicID(ctx, req.GetCustomerId())
				if err != nil {
					return nil, err
				}
				create.Customer = customer
			}
			for _, t := range req.GetWalletTenders() {
				w, err := s.visibleWallet(ctx, viewer, t.GetWalletId())
				if err != nil {
					return nil, err
				}
				create.Tenders = append(create.Tenders, order.WalletTender{Wallet: w, Amount: t.GetAmount()})
			}

			result, err := s.orders.Create(ctx, create)
			if err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action: "order.created", ResourceType: "order", ResourceID: result.Order.PublicID,
				ProductID: &productID,
				After: map[string]any{"external_ref": result.Order.ExternalRef, "total": result.Order.Total,
					"gateway_amount": result.Order.GatewayAmount, "status": result.Order.Status},
			}); err != nil {
				return nil, err
			}
			out, err := s.orderDetail(ctx, result.Order)
			if err != nil {
				return nil, err
			}
			return &openpay_v1.CreateOrderResponse{Order: out}, nil
		})
}

func (s *Service) GetOrder(ctx context.Context, req *openpay_v1.GetOrderRequest) (*openpay_v1.GetOrderResponse, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	caller, _ := appcontext.CallerFrom(ctx)
	var scope *filter.ProductScope
	if caller.IsService() {
		scope = filter.OnlyProducts(caller.ProductID)
	} else {
		if err := auth.RequireOperator(ctx, auth.PermPaymentsRead); err != nil {
			return nil, err
		}
		var err error
		if scope, err = s.callerScope(ctx); err != nil {
			return nil, err
		}
	}
	o, err := s.repo.GetOrderByPublicID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if err := requireVisible(scope, &o.ProductID, "order", req.GetId()); err != nil {
		return nil, err
	}
	out, err := s.orderDetail(ctx, o)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.GetOrderResponse{Order: out}, nil
}

func (s *Service) orderDetail(ctx context.Context, o *dao.Order) (*openpay_v1.Order, error) {
	out := &openpay_v1.Order{
		Id: o.PublicID, ExternalRef: o.ExternalRef, InvoiceRef: o.InvoiceRef, ProductId: o.ProductPublicID,
		CustomerId: deref(o.CustomerPublicID), Currency: o.Currency,
		Subtotal: o.Subtotal, Discount: o.Discount, Tax: o.Tax, Total: o.Total, TaxRate: o.TaxRate,
		TaxBreakdownProvided: o.TaxBreakdownProvided, Status: toProtoOrderStatus(o.Status),
		FailureReason: deref(o.FailureReason), GatewayAmount: o.GatewayAmount,
		ExpiresAt: timestamppb.New(o.ExpiresAt), CreatedAt: timestamppb.New(o.CreatedAt),
	}
	if o.PaidAt != nil {
		out.PaidAt = timestamppb.New(*o.PaidAt)
	}

	lines, err := s.repo.ListOrderLines(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		out.Lines = append(out.Lines, &openpay_v1.OrderLine{ItemId: deref(l.ItemPublicID), Description: l.Description,
			Quantity: int32(l.Quantity), UnitAmount: l.UnitAmount, Amount: l.Amount})
	}
	tenders, err := s.repo.ListOrderTenders(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range tenders {
		out.Tenders = append(out.Tenders, &openpay_v1.OrderTender{Kind: string(t.Kind),
			WalletId: deref(t.WalletPublicID), Amount: t.Amount, Status: string(t.Status)})
	}

	refunds, err := s.repo.ListOrderRefunds(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	for _, x := range refunds {
		parts, err := s.repo.ListOrderRefundParts(ctx, x.ID)
		if err != nil {
			return nil, err
		}
		out.Refunds = append(out.Refunds, toProtoOrderRefund(x, parts))
	}

	if o.GatewayAmount > 0 {
		p, err := s.repo.GetOrderPayment(ctx, o.ID)
		if err != nil && !apperrors.Is(err, apperrors.NotFound) {
			return nil, err
		}
		if p != nil {
			out.PaymentId = p.PublicID
			detail, err := s.paymentDetail(ctx, p, false)
			if err != nil {
				return nil, err
			}
			if o.Status == dao.OrderPendingPayment {
				out.CheckoutUrl = detail.GetCheckoutUrl()
			}
		}
	}
	return out, nil
}

func toProtoOrderStatus(s dao.OrderStatus) openpay_v1.OrderStatus {
	switch s {
	case dao.OrderPendingPayment:
		return openpay_v1.OrderStatus_ORDER_STATUS_PENDING_PAYMENT
	case dao.OrderPaid:
		return openpay_v1.OrderStatus_ORDER_STATUS_PAID
	case dao.OrderPartiallyRefunded:
		return openpay_v1.OrderStatus_ORDER_STATUS_PARTIALLY_REFUNDED
	case dao.OrderRefunded:
		return openpay_v1.OrderStatus_ORDER_STATUS_REFUNDED
	case dao.OrderFailed:
		return openpay_v1.OrderStatus_ORDER_STATUS_FAILED
	case dao.OrderCancelled:
		return openpay_v1.OrderStatus_ORDER_STATUS_CANCELLED
	default:
		return openpay_v1.OrderStatus_ORDER_STATUS_UNSPECIFIED
	}
}

func toProtoItem(it *dao.Item) *openpay_v1.Item {
	return &openpay_v1.Item{Id: it.PublicID, Code: it.Code, Name: it.Name, ReferencePrice: it.ReferencePrice,
		Currency: it.Currency, TaxClass: it.TaxClass, CreatedAt: timestamppb.New(it.CreatedAt)}
}

func (s *Service) RefundOrder(ctx context.Context, req *openpay_v1.RefundOrderRequest) (*openpay_v1.RefundOrderResponse, error) {
	// A product refunds its own orders; central ops can refund any. Unlike a
	// top-up refund, this cannot leak closed-loop money: every share goes
	// back the way it came, or becomes store credit.
	caller, _ := appcontext.CallerFrom(ctx)
	scope := filter.AllProducts()
	if caller.IsService() {
		scope = filter.OnlyProducts(caller.ProductID)
	} else if err := auth.RequirePlatformOperator(ctx, auth.PermRefundsCreate); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	return idempotent(ctx, s.repo, "RefundOrder", req,
		func(ctx context.Context) (*openpay_v1.RefundOrderResponse, error) {
			o, err := s.repo.GetOrderByPublicID(ctx, req.GetId())
			if err != nil {
				return nil, err
			}
			if err := requireVisible(scope, &o.ProductID, "order", req.GetId()); err != nil {
				return nil, err
			}
			refundReq := order.RefundRequest{
				OrderID: o.ID, Amount: req.GetAmount(), Destination: req.GetDestination(),
				ReasonCode: req.GetReasonCode(), Memo: req.GetMemo(), RequestedBy: caller.UserID,
			}
			if caller.IsService() {
				refundReq.RequestedBy = caller.CredentialID
			}
			if req.GetTaxBreakdownProvided() {
				refundReq.Breakdown = &order.Breakdown{Subtotal: req.GetSubtotal(), Discount: req.GetDiscount(), Tax: req.GetTax()}
			}
			refund, parts, err := s.orders.Refund(ctx, refundReq)
			if err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action: "order.refunded", ResourceType: "order", ResourceID: o.PublicID, ProductID: &o.ProductID,
				After: map[string]any{"refund_id": refund.PublicID, "amount": refund.Amount, "tax": refund.Tax,
					"destination": refund.Destination, "reason_code": refund.ReasonCode},
			}); err != nil {
				return nil, err
			}
			current, err := s.repo.GetOrderByPublicID(ctx, o.PublicID)
			if err != nil {
				return nil, err
			}
			out, err := s.orderDetail(ctx, current)
			if err != nil {
				return nil, err
			}
			return &openpay_v1.RefundOrderResponse{Order: out, Refund: toProtoOrderRefund(refund, parts)}, nil
		})
}

func toProtoOrderRefund(x *dao.OrderRefund, parts []*dao.OrderRefundPart) *openpay_v1.OrderRefund {
	out := &openpay_v1.OrderRefund{
		Id: x.PublicID, Amount: x.Amount, Subtotal: x.Subtotal, Discount: x.Discount, Tax: x.Tax,
		TaxBreakdownProvided: x.TaxBreakdownProvided, Destination: x.Destination,
		ReasonCode: x.ReasonCode, Memo: x.Memo, CreatedAt: timestamppb.New(x.CreatedAt),
	}
	for _, p := range parts {
		out.Parts = append(out.Parts, &openpay_v1.OrderRefundPart{Amount: p.Amount,
			WalletId: deref(p.WalletPublicID), RefundId: deref(p.RefundPublicID)})
	}
	return out
}

func (s *Service) CancelOrder(ctx context.Context, req *openpay_v1.CancelOrderRequest) (*openpay_v1.CancelOrderResponse, error) {
	productID, err := auth.RequireService(ctx)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	o, err := s.repo.GetOrderByPublicID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if err := requireVisible(filter.OnlyProducts(productID), &o.ProductID, "order", req.GetId()); err != nil {
		return nil, err
	}
	reason := req.GetReason()
	if reason == "" {
		reason = "cancelled by the product"
	}
	current, err := s.orders.Cancel(ctx, o, reason)
	if err != nil {
		return nil, err
	}
	out, err := s.orderDetail(ctx, current)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.CancelOrderResponse{Order: out}, nil
}

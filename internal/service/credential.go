package service

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/ids"
	"github.com/gofreego/openpay/pkg/secrets"
)

// keyIDPrefix marks OpenPay keys so one found in a log or a config file is
// recognisable as ours.
const keyIDPrefix = "opk"

// CreateServiceCredential issues a credential for a product's backend.
//
// The secret is returned once, here, and never again: only its hash is stored.
// That is the point — a database leak hands over no working credentials.
func (s *Service) CreateServiceCredential(ctx context.Context, req *openpay_v1.CreateServiceCredentialRequest) (*openpay_v1.CreateServiceCredentialResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermCredentialsWrite); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	generated, err := secrets.Generate(keyIDPrefix)
	if err != nil {
		return nil, err
	}

	// Deliberately not wrapped in idempotent(): the secret is returned once and
	// recording it for replay would put a working credential in the
	// idempotency table in clear text. A duplicate call issues a second
	// credential instead, which is visible, revocable, and harmless.
	credential := &dao.ServiceCredential{
		PublicID:   ids.New(ids.ServiceCredential),
		Name:       req.GetName(),
		KeyID:      generated.KeyID,
		SecretHash: generated.SecretHash,
		Status:     dao.CredentialActive,
	}

	err = s.repo.WithTx(ctx, func(ctx context.Context) error {
		product, err := s.repo.GetProductByPublicID(ctx, req.GetProductId())
		if err != nil {
			return err
		}
		credential.ProductID = product.ID

		if err := s.repo.CreateCredential(ctx, credential); err != nil {
			return err
		}
		return s.audit(ctx, auditParams{
			Action:       "credential.created",
			ResourceType: "service_credential",
			ResourceID:   credential.PublicID,
			ProductID:    &product.ID,
			// The audit record names the key, never the secret.
			After: map[string]any{"key_id": credential.KeyID, "name": credential.Name},
		})
	})
	if err != nil {
		return nil, err
	}

	return &openpay_v1.CreateServiceCredentialResponse{
		Credential: toProtoCredential(credential, req.GetProductId()),
		Secret:     generated.Secret,
	}, nil
}

func (s *Service) ListServiceCredentials(ctx context.Context, req *openpay_v1.ListServiceCredentialsRequest) (*openpay_v1.ListServiceCredentialsResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermCredentialsWrite); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	product, err := s.repo.GetProductByPublicID(ctx, req.GetProductId())
	if err != nil {
		return nil, err
	}

	credentials, err := s.repo.ListCredentials(ctx, product.ID)
	if err != nil {
		return nil, err
	}

	response := &openpay_v1.ListServiceCredentialsResponse{}
	for _, credential := range credentials {
		response.Credentials = append(response.Credentials, toProtoCredential(credential, req.GetProductId()))
	}
	return response, nil
}

func (s *Service) RevokeServiceCredential(ctx context.Context, req *openpay_v1.RevokeServiceCredentialRequest) (*openpay_v1.RevokeServiceCredentialResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermCredentialsWrite); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	// Revocation is not made idempotency-key-dependent: revoking twice is the
	// caller's intent already satisfied, and requiring a key would stand
	// between an operator and shutting off a leaked credential.
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		if err := s.repo.RevokeCredential(ctx, req.GetId()); err != nil {
			return err
		}
		return s.audit(ctx, auditParams{
			Action:       "credential.revoked",
			ResourceType: "service_credential",
			ResourceID:   req.GetId(),
		})
	})
	if err != nil {
		return nil, err
	}

	return &openpay_v1.RevokeServiceCredentialResponse{
		Credential: &openpay_v1.ServiceCredential{
			Id:     req.GetId(),
			Status: openpay_v1.CredentialStatus_CREDENTIAL_STATUS_REVOKED,
		},
	}, nil
}

func toProtoCredential(c *dao.ServiceCredential, productID string) *openpay_v1.ServiceCredential {
	out := &openpay_v1.ServiceCredential{
		Id:        c.PublicID,
		ProductId: productID,
		Name:      c.Name,
		KeyId:     c.KeyID,
		Status:    toProtoCredentialStatus(c.Status),
		CreatedAt: timestamppb.New(c.CreatedAt),
	}
	if c.LastUsedAt != nil {
		out.LastUsedAt = timestamppb.New(*c.LastUsedAt)
	}
	if c.RevokedAt != nil {
		out.RevokedAt = timestamppb.New(*c.RevokedAt)
	}
	return out
}

func toProtoCredentialStatus(s dao.CredentialStatus) openpay_v1.CredentialStatus {
	if s == dao.CredentialRevoked {
		return openpay_v1.CredentialStatus_CREDENTIAL_STATUS_REVOKED
	}
	return openpay_v1.CredentialStatus_CREDENTIAL_STATUS_ACTIVE
}

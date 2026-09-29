// Reason codes, mirroring the server's fixed lists. The server validates
// them; these only populate the ceremony's select.
export const Reasons = {
  grant: ['promotion', 'referral', 'cashback', 'loyalty', 'compensation'], // internal/wallet GrantReasons
  adjust: ['goodwill', 'error_correction', 'fraud_recovery', 'migration'], // internal/wallet AdjustReasons
  refund: ['customer_request', 'duplicate_payment', 'fraud', 'service_issue', 'unapplied_payment'], // internal/payment RefundReasons
  recon: ['provider_error', 'reference_mismatch', 'timing', 'bank_adjustment', 'unrecoverable', 'other'], // internal/recon ResolutionReasons
} as const

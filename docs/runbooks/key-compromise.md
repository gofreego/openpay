# Key compromise

**Signal:** a secret was committed, pasted, logged, sent to the wrong place
or held by someone who has left. Treat "might have leaked" as leaked.

First, **which secret?** They fail differently and are rotated differently.

| Secret | What an attacker can do | Section |
|---|---|---|
| Service credential (`Bearer <key_id>.<secret>`) | Act as that product's backend: open wallets, create payments, grant | [1](#1-service-credential) |
| Provider API keys | Create refunds and payouts at the provider directly | [2](#2-provider-api-keys) |
| Provider webhook secret | Forge webhooks | [3](#3-webhook-secret) |
| Field encryption key | Read bank account numbers from a DB dump or backup | [4](#4-field-encryption-key) |
| Database credentials | Everything | [5](#5-database-credentials) |
| Direct network access to OpenPay | Forge operator identity | [6](#6-operator-headers) |

## 1. Service credential

Credentials resolve to exactly one product, are stored only as hashes and
can be revoked instantly.

1. Issue a replacement for the same product:
   `POST $OPENPAY/openpay/v1/credentials {"product_id": "...", "name": "rotated INC-123"}`
   (`openpay:credentials:write`). The secret is shown **once**.
2. Deploy it to the product backend.
3. Revoke the old one: `POST $OPENPAY/openpay/v1/credentials/{id}/revoke`.
   Takes effect on the next request; the product's traffic moves to the new
   credential.
4. Review what the old credential did. `last_used_at` on the credential
   shows whether it was used after the leak; audited actions record it as
   `actor_type = 'service'`, `actor_id = <credential id>` in `audit_log`;
   payments, grants and transfers it made are the product's records created
   since the leak (`GET /payments?product_id=…`, wallet statements).

## 2. Provider API keys

1. Roll the key in the provider's dashboard; put the new one in the secret
   store; deploy.
2. In the provider dashboard, review refunds and payouts since the leak. Any
   we did not initiate will surface anyway as reconciliation breaks
   (`missing_in_ledger`) — see [recon](recon-break-backlog.md) — but do not
   wait for settlement to find them.

## 3. Webhook secret

A forged webhook cannot move money on its own: OpenPay treats webhooks as
hints and asks the provider for the real state before applying anything.
The risk is noise and load, not theft. Still:

1. Roll the secret in the provider dashboard and in config; deploy.
2. During the switch the provider may sign with the new secret before every
   instance has it; those deliveries get `401` and the provider redelivers.
   The poller covers any gap.

## 4. Field encryption key

Bank account numbers are sealed with AES-256-GCM under a key id
(`Service.Encryption`). Rotation is built in:

1. Generate a key: `head -c 32 /dev/urandom | base64`. Add it to
   `Service.Encryption.Keys` under a new id in the secret store, and set
   `CurrentKeyID` to it. **Keep the old key in `Keys` for now.**
2. Deploy. New account numbers are sealed under the new key. At startup the
   worker **reseals** every stored account number under it and recomputes
   its fingerprint; watch for
   `resealed N bank account numbers under key <new id>`.
3. Confirm nothing is left under the old key:
   ```sql
   SELECT COUNT(*) FROM beneficiaries
   WHERE account_number IS NOT NULL AND account_number NOT LIKE 'v1:<new id>:%';
   ```
   must be 0. If the worker logged
   `beneficiaries [...] duplicate another account of the same customer`,
   those rows were registered twice during the switch — merge them, then
   restart the worker.
4. Remove the old key from config and deploy. It is now useless to anyone
   holding it — except against **backups taken before step 3**, which still
   hold values under the old key. Treat those backups as exposed; expire
   them per the retention policy.

## 5. Database credentials

Rotate the database password (Postgres `ALTER ROLE … PASSWORD`, then the
secret store, then deploy). Assume the attacker read everything: account
numbers are sealed (see 4 — rotate the field key too if config was exposed
alongside), but names, amounts and history are not. This is a data breach;
follow the incident process for notifying compliance.

## 6. Operator headers

OpenPay trusts `x-user-id` and `x-user-perms` because **only opengate can
reach it**: opengate validates the OpenAuth session and injects them. If
OpenPay's port is reachable from anywhere else, anyone can claim any
permission with a header.

1. Close the path: OpenPay must accept traffic only from opengate and from
   product backends' network (who authenticate with credentials, not
   headers).
2. Review `audit_log` for operator actions whose `actor_id` is not a real
   OpenAuth user, or that did not come through opengate's logs.

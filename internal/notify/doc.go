// Package notify provides the universal email-notification client.
//
// See notify.go for the design summary. Vendors are Resend (Bearer token)
// and Brevo (api-key header). Idempotency is required via Email.IdempotencyKey;
// retries classify 4xx as ErrPermanent (fail-fast) and 5xx as ErrTransient
// with up to 3 retries; exhaustion returns ErrDeadLetter. The dispatcher
// round-robins between Resend and Brevo and falls back to the other vendor
// on ErrDeadLetter.
//
// This package is the single sink called by helixon-platform/fleet agents
// when sending notification email. Raw curl/fetch/requests to
// *.resend.com or *.brevo.com are blocked by helix-dev-tools hook
// guard-email; code that needs to send email must depend on this package.
package notify

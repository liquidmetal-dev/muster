// Package webhook is the webhook intake (design §5). It accepts GitHub
// workflow_job deliveries and hands queued, in_progress and completed jobs
// that match a pool to the demand registry.
//
// Each delivery is checked in order: body size, the X-Hub-Signature-256
// HMAC (requirements INTAKE-1, SEC-1), the event and delivery headers, the
// payload, the pool match (INTAKE-7), and last the delivery id, which is
// remembered for DeliveryTTL so that a redelivery is ignored (INTAKE-3).
// Only a delivery that passes every check is recorded, so a delivery that
// failed can still succeed when GitHub sends it again. The hand-off to the
// registry does not wait for VM acquisition (INTAKE-4).
package webhook

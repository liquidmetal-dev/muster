// Package pools reconciles muster's configured pools into battery's
// PoolAdmin at startup (design section 6, requirements POOL-2, POOL-3,
// POOL-4 and POOL-7). The configuration is the only source of a pool's
// spec, and a prune never forces
// (docs/adr/2026-11-04-muster-owns-its-pool-specs.md).
//
// HostCheck is the config.Checker that rejects pool hosts battery does not
// know. Its Resolve feeds the flintlock exec checker from the same host
// list. Reconcile then creates, updates and prunes pools.
package pools

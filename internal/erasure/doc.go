/*
Package erasure implements Reed-Solomon erasure coding.The data, parity, and total shard counts are NOT package-level constants — they are injected from the active NetworkProfile via NewEngine(profile).

This allows demo mode (DataShards=3, TotalShards=5) and production mode (DataShards=16, TotalShards=56) to use identical code paths. (ADR-031)

ShardSize (262,144 bytes) IS a package-level constant and must not change between modes. A compiler-enforced test verifies this.

Ref: ADR-003
*/
package erasure

package p2p

// dhtKeyNamespace is the DHT validator/protocol namespace for all Vyomanaut chunk
// content-address records (IC §12, ADR-001, ARCH §13 §DHT configuration).
//
// SOLE DEFINITION: This constant is the only place in the repository where the string
// "/vyomanaut/dht-key/1.0.0" may appear (MVP §8.2). All other files must reference
// this constant — never inline the string literal.
//
// UPGRADE GUARD: TestDHTKeyValidatorPersists (CI check 5, MVP §8.4) verifies this
// namespace survives every dependency upgrade.
//
// BREAKING CHANGE: Modifying this string is a network-breaking change — all provider
// daemons must upgrade simultaneously before any chunk lookup succeeds (IC §13).
const dhtKeyNamespace = "/vyomanaut/dht-key/1.0.0"

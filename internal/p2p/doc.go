/* 
Package p2p provides peer identity management, transport stack initialisation, and the Kademlia DHT
interface. Goroutine-safe after construction.

Included components:

  - libp2p host
  - QUIC/TCP transports
  - DHT (custom HMAC key validator)
  - NAT traversal
  - Heartbeat

Ref: ADR-021, ADR-001
*/
package p2p
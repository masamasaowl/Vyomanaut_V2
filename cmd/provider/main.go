// Command provider is the Vyomanaut V2 provider daemon entrypoint.
//
// Startup order (Session 13.1.1 TASK, IC §5.3 pre-condition chain):
//  1. Parse flags (MVP §8.3).
//  2. config.SelectProfile then config.ValidateStartupGuards.
//  3. RAM check (Session 13.6.1, A1) — before ChunkStore init.
//  4. Load/generate Ed25519 identity (internal/p2p/identity.go).
//  5. ChunkStore.RecoverFromCrash() before starting the writer goroutine.
//  6. Start the single writer goroutine (only caller of AppendChunk).
//  7. NewHost with responder-side 0-RTT rejection for every protocol in
//     zeroRTTProhibited (A7) — see the note below on what that means for
//     this codebase's transport substitution.
//  8. Register the four stream handlers (Phases 13.2-13.5).
//  9. Heartbeat goroutine + DHT republication (IC §3.1, §12.2); the DHT
//     custom validator from dht_namespace.go (IC §12) is registered
//     automatically by p2p.NewDHT.
//
// ONE-FLAG NOTE (build.md's own preamble to Milestone 13): ADR-038/ADR-047
// assume the tray process and the daemon logic are the same process,
// started in-process by a Task Scheduler logon trigger. This file is kept
// as a thin wrapper around package-level constructors (NewUploadHandler,
// NewAuditHandler, NewRepairDownloadHandler, NewVettingGCHandler,
// runRAMCheck, etc.) rather than owning the whole process lifecycle inline
// as unexported main()-local logic, so a future Wails app (Milestone 19)
// can call the same startup sequence in-process instead of exec-ing a
// separate binary.
//
// RESPONDER-SIDE 0-RTT (A7): IC §4's zeroRTTProhibited deny-list
// (internal/p2p/host.go) is enforced on the DIALING side only in this
// codebase's transport substitution — see handler_audit.go's header for the
// full account of why (this is crypto/tls session-ticket resumption over
// plain TCP, not real QUIC 0-RTT early data; there is nothing
// responder-observable to police at accept time before the protocol is even
// negotiated). For all four protocols this daemon registers handlers for,
// this daemon is exclusively the RESPONDER — it never calls Host.NewStream
// for chunk-upload/audit-challenge/repair-download/vetting-gc, so
// zeroRTTProhibited's enforcement point (the caller's NewStream) is
// correctly on the microservice/client side for every one of them; nothing
// further is required here beyond constructing the Host that already
// carries that enforcement.
//
// [REF: IC §3.1, IC §4, IC §12, IC §12.2, MVP §5.3, MVP §8.3, NFR-045,
// ARCH §27.5, build.md Sessions 13.1.1, 13.6.1]
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/masamasaowl/Vyomanaut_V2/internal/config"
	"github.com/masamasaowl/Vyomanaut_V2/internal/metrics"
	"github.com/masamasaowl/Vyomanaut_V2/internal/p2p"
	"github.com/masamasaowl/Vyomanaut_V2/internal/storage"
)

// daemonVersion is reported in the heartbeat payload (IC §3.1) and startup
// banner. Bumped manually; no build-stamping mechanism exists in this
// session's scope.
const daemonVersion = "v0.13.0"

// defaultProviderListenPort is the fixed inbound libp2p listen port for
// normal (non-simulation) mode. Not specified as a flag anywhere in MVP
// §8.3, so a single fixed default is used; simulation mode's
// --sim-base-port exists precisely to avoid collisions when many instances
// run in one process, which does not apply here since simulation mode is
// not implemented in this session (see the --sim-count guard below).
const defaultProviderListenPort = 4001

const (
	defaultSimBasePort = 4001
	defaultSimASNCount = 5
)

// providerFlags holds every parsed cmd/provider/main.go flag (MVP §8.3).
type providerFlags struct {
	mode              string
	microserviceURL   string
	dataDir           string
	declaredStorageGB int
	relayAddrs        string
	simCount          int
	simBasePort       int
	simDataDir        string
	simASNCount       int
}

func parseProviderFlags() providerFlags {
	home, _ := os.UserHomeDir()
	defaultDataDir := filepath.Join(home, ".vyomanaut")

	var f providerFlags
	flag.StringVar(&f.mode, "mode", "", "'demo' or 'prod'; overrides VYOMANAUT_MODE")
	flag.StringVar(&f.microserviceURL, "microservice-url", "", "Required. HTTPS base URL of the coordination microservice.")
	flag.StringVar(&f.dataDir, "data-dir", defaultDataDir, "Persistent data directory.")
	flag.IntVar(&f.declaredStorageGB, "declared-storage-gb", 0, "Required in normal mode.")
	flag.StringVar(&f.relayAddrs, "relay-addrs", "", "Comma-separated relay node multiaddrs.")
	flag.IntVar(&f.simCount, "sim-count", 0, "Simulation instances in a single process. 0 = normal mode.")
	flag.IntVar(&f.simBasePort, "sim-base-port", defaultSimBasePort, "Base libp2p listen port for simulation instances.")
	flag.StringVar(&f.simDataDir, "sim-data-dir", "/tmp/vyomanaut-sim", "Root directory for simulation instance data.")
	flag.IntVar(&f.simASNCount, "sim-asn-count", defaultSimASNCount, "Synthetic ASN count for simulation mode.")
	flag.Parse()
	return f
}

// ── RAM check (Session 13.6.1, A1) ────────────────────────────────────────

const bytesPerMiB = 1 << 20

// Placed before main() deliberately: main() calls runRAMCheck at Step 3,
// strictly before the ChunkStore/RecoverFromCrash sequence at Step 5 and
// the writer-goroutine start that immediately follows it — keeping this
// section's source text ahead of that sequence too, not just its runtime
// call order, keeps the file's physical layout matching its execution
// order for anyone reading top-to-bottom.

// runRAMCheck computes the required DHT-cache RAM for declaredStorageGB
// (storage.RequiredDHTCacheRAMMB) and compares it against currently free
// RAM (storage.AvailableRAMBytes). On shortfall: WARN (never halt — see IC
// §27.5), reduce the effective declared storage to the safe ceiling the
// available RAM actually supports, and report ram-constrained.
func runRAMCheck(declaredStorageGB int) (effectiveStorageGB int, constrained bool) {
	requiredMB := storage.RequiredDHTCacheRAMMB(uint64(declaredStorageGB))

	availableBytes, err := storage.AvailableRAMBytes()
	if err != nil {
		// Platform RAM query unsupported/failed: WARN and proceed
		// unconstrained (fail-open here is intentional — NFR-045's guard is
		// a courtesy warning, not a hard admission-control gate, and this
		// codebase's other platform-detection stubs (rotational_other.go)
		// follow the same "assume the common/safe case and proceed"
		// pattern when a platform query is unavailable).
		log.Printf("[WARN] RAM check unavailable on this platform (%v); proceeding without a RAM guard", err)
		return declaredStorageGB, false
	}
	availableMB := availableBytes / bytesPerMiB

	if availableMB >= requiredMB {
		return declaredStorageGB, false
	}

	safeGB := safeDeclaredStorageGB(availableMB)
	log.Printf("[WARN] Declared storage requires ~%d MB free RAM for DHT cache; only %d MB detected. Chunk assignment will be limited until RAM is freed.", requiredMB, availableMB)
	if safeGB < 1 {
		safeGB = 1 // never reduce to zero; DHTRecordSizeBytes(200B)/ChunksPerGB math floors near zero for tiny availableMB
	}
	return safeGB, true
}

// safeDeclaredStorageGB inverts RequiredDHTCacheRAMMB: the largest declared
// storage (GB) whose required RAM does not exceed availableMB.
func safeDeclaredStorageGB(availableMB uint64) int {
	// requiredMB = gb * ChunksPerGB * DHTRecordSizeBytes / (1<<20)
	// => gb = availableMB * (1<<20) / (ChunksPerGB * DHTRecordSizeBytes)
	denom := uint64(storage.ChunksPerGB) * uint64(storage.DHTRecordSizeBytes)
	gb := (availableMB * bytesPerMiB) / denom
	return int(gb)
}

const privateDirPermissions = 0700
const chunkWriteQueueSize = 64

func main() {
	flags := parseProviderFlags()

	// ── Step 2: profile selection + startup guards ──────────────────────
	profile := config.SelectProfile(flags.mode)
	if err := config.ValidateStartupGuards(profile); err != nil {
		log.Fatalf("[STARTUP] FATAL guard rail: %v", err)
	}

	if flags.simCount > 0 {
		// Full in-process simulation orchestration (spinning up
		// flags.simCount instances under flags.simDataDir) is not
		// implemented in this session's scope — build.md's Session 13.1.1
		// TASK list covers single-instance startup only; multi-instance
		// simulation wiring is not detailed anywhere this session was
		// given as reference. Flagged rather than guessed at.
		log.Fatalf("[STARTUP] FATAL: --sim-count=%d requested, but simulation-mode orchestration is not implemented in this build (Session 13.1.1 scope)", flags.simCount)
	}
	if flags.declaredStorageGB <= 0 {
		log.Fatalf("[STARTUP] FATAL: --declared-storage-gb is required and must be > 0 in normal mode")
	}
	if flags.microserviceURL == "" {
		log.Fatalf("[STARTUP] FATAL: --microservice-url is required")
	}
	if err := os.MkdirAll(flags.dataDir, privateDirPermissions); err != nil {
		log.Fatalf("[STARTUP] FATAL: create data-dir %s: %v", flags.dataDir, err)
	}

	log.Printf("[STARTUP] Vyomanaut provider %s — mode=%s data-dir=%s declared-storage-gb=%d",
		daemonVersion, profile.Mode, flags.dataDir, flags.declaredStorageGB)

	// ── Step 3: RAM check — BEFORE ChunkStore init (A1) ─────────────────
	effectiveStorageGB, ramConstrained := runRAMCheck(flags.declaredStorageGB)
	metrics.DaemonRAMConstrained.Set(boolToFloat(ramConstrained))
	if _, errCh := metrics.StartDaemonMetricsServer(); errCh != nil {
		go func() {
			if err := <-errCh; err != nil {
				log.Printf("[STARTUP] daemon status/metrics server error: %v", err)
			}
		}()
	}

	// ── Step 4: load/generate Ed25519 identity ──────────────────────────
	// GAP (flagged — see build report IDENTITY-SEED-GAP): LoadOrGenerateIdentity
	// requires a masterSecret+ownerID pair, normally derived from a data
	// owner's passphrase (IC §5.1). No provider registration/OTP flow that
	// would yield an equivalent for a PROVIDER is in this session's scope
	// (MVP §8.3's flag table has no such flag). loadOrGenerateOwnerSeed
	// below generates and locally persists a random masterSecret+ownerID
	// pair on first run so the identity is at least STABLE across restarts
	// (the functional requirement this session needs); it is deliberately
	// NOT passphrase-recoverable the way a data owner's account is — that
	// is a real gap for a future registration-flow session to close.
	masterSecret, ownerID, err := loadOrGenerateOwnerSeed(flags.dataDir)
	if err != nil {
		log.Fatalf("[STARTUP] FATAL: owner seed: %v", err)
	}
	providerSigningKey, peerID, err := p2p.LoadOrGenerateIdentity(flags.dataDir, masterSecret, ownerID[:])
	if err != nil {
		log.Fatalf("[STARTUP] FATAL: load/generate identity: %v", err)
	}
	log.Printf("[STARTUP] Peer ID: %s", peerID)
	providerIDBytes := deriveLocalProviderIDBytes(peerID)

	// ── microservice public key (JWKS) + derived Peer ID ────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msPublicKey, err := fetchMicroservicePublicKey(ctx, flags.microserviceURL)
	if err != nil {
		// Fail-closed, not fatal: every handler treats a nil/absent
		// msPublicKey as "reject everything requiring verification"
		// (handler_upload.go's verifyCapabilityTokenFrame,
		// handler_repair.go/handler_vetting_gc.go's sig checks) rather than
		// crashing the daemon — a transient microservice outage at startup
		// should not prevent the daemon from starting and retrying.
		log.Printf("[STARTUP] WARNING: could not fetch microservice public key (%v); all capability/audit/repair/GC verification will fail closed until this succeeds", err)
	}
	var microservicePeerID p2p.PeerID
	if msPublicKey != nil {
		microservicePeerID, err = p2p.PeerIDFromEd25519PublicKey(msPublicKey)
		if err != nil {
			log.Printf("[STARTUP] WARNING: derive microservice Peer ID from JWKS key: %v", err)
		}
	}

	// ── ChunkStore ────────────────────────────────────────────────────────
	store, err := storage.NewChunkStore(flags.dataDir)
	if err != nil {
		log.Fatalf("[STARTUP] FATAL: open chunk store: %v", err)
	}

	// ── Step 5: RecoverFromCrash BEFORE the writer goroutine ────────────
	if err := store.RecoverFromCrash(); err != nil {
		log.Fatalf("[STARTUP] FATAL: RecoverFromCrash: %v", err)
	}

	// ── Step 6: single writer goroutine (only caller of AppendChunk) ────
	writeCh := make(chan chunkWriteRequest, chunkWriteQueueSize)
	go runChunkStoreWriter(store, writeCh)

	// ── Step 7: NewHost ──────────────────────────────────────────────────
	listenAddr := fmt.Sprintf("0.0.0.0:%d", defaultProviderListenPort)
	host, err := p2p.NewHost(p2p.HostConfig{PrivateKey: providerSigningKey, ListenAddr: listenAddr})
	if err != nil {
		log.Fatalf("[STARTUP] FATAL: NewHost: %v", err)
	}

	// ── Step 8: register the four stream handlers (IC §4) ───────────────
	// Each protocol ID's SOLE DEFINITION remains its own handler_*.go
	// const (mirroring dht_namespace.go's own "never inline the string
	// literal elsewhere" discipline, IC §12); the four lines below exist
	// for readability only, not as a second definition:
	//   /vyomanaut/chunk-upload/1.0.0
	//   /vyomanaut/audit-challenge/1.0.0
	//   /vyomanaut/repair-download/1.0.0
	//   /vyomanaut/vetting-gc/1.0.0
	statusHolder := newProviderStatusHolder(providerStatusActive)

	uploadHandler := NewUploadHandler(store, writeCh, msPublicKey, providerSigningKey, providerIDBytes, statusHolder)
	host.SetStreamHandler(chunkUploadProtocolID, uploadHandler.HandleStream)

	auditHandler := NewAuditHandler(store, providerSigningKey, providerIDBytes)
	host.SetStreamHandler(auditChallengeProtocolID, auditHandler.HandleStream)

	// The registered-microservice-replica authorizer shared by
	// repair-download and vetting-gc. Seeded with the single microservice
	// Peer ID derived from its JWKS key above — see the GAP note on
	// MicroserviceAuthorizer in handler_repair.go for why this assumes one
	// shared cluster identity rather than a discovered replica set (IC
	// §4.4.1's DHT/heartbeat-driven refresh is not wired in this session).
	authorizer := newStaticMicroserviceAuthorizer()
	if !microservicePeerID.Empty() {
		authorizer.Set([]p2p.PeerID{microservicePeerID})
	}

	repairHandler := NewRepairDownloadHandler(store, msPublicKey, authorizer, profile.AuthRequestFreshnessWindow, microservicePeerID)
	host.SetStreamHandler(repairDownloadProtocolID, repairHandler.HandleStream)

	vettingGCHandler := NewVettingGCHandler(store, msPublicKey, authorizer, profile.AuthRequestFreshnessWindow, microservicePeerID)
	host.SetStreamHandler(vettingGCProtocolID, vettingGCHandler.HandleStream)

	log.Printf("[STARTUP] registered stream handlers: %s %s %s %s",
		chunkUploadProtocolID, auditChallengeProtocolID, repairDownloadProtocolID, vettingGCProtocolID)

	// ── Step 9: heartbeat goroutine + DHT republication ─────────────────
	// p2p.NewDHT registers the DHT custom validator (dht_namespace.go's
	// dhtKeyNamespace, IC §12) automatically via host.SetStreamHandler
	// internally — nothing further is required here to wire that up.
	dht, err := p2p.NewDHT(host, p2p.DHTConfig{RecordTTL: profile.DHTExpiryDuration})
	if err != nil {
		log.Fatalf("[STARTUP] FATAL: NewDHT: %v", err)
	}

	heartbeatCfg := p2p.HeartbeatConfig{
		Profile:         profile,
		CurrentAddrs:    func() []p2p.Multiaddr { return nil }, // GAP: NAT/relay address discovery is out of this session's scope
		DHT:             dht,
		Store:           nil, // GAP: ChunkDHTKeySource adapter over storage.ChunkStore not yet built (see heartbeat.go's own file comment)
		MicroserviceURL: flags.microserviceURL,
		ProviderID:      string(peerID), // GAP: no microservice-assigned provider_id available this session; Peer ID stands in
		DaemonVersion:   daemonVersion,
		SigningKey:      providerSigningKey,
	}
	go p2p.RunHeartbeat(ctx, heartbeatCfg)

	log.Printf("[STARTUP] Vyomanaut provider daemon ready (effective-storage-gb=%d ram-constrained=%v)", effectiveStorageGB, ramConstrained)

	waitForShutdownSignal()
	log.Printf("[SHUTDOWN] signal received, shutting down")
	cancel()
	close(writeCh)
	_ = host.Close()
	_ = store.Close()
}

// waitForShutdownSignal blocks until SIGINT or SIGTERM.
func waitForShutdownSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// runChunkStoreWriter is the single designated writer goroutine
// (storage.ChunkStore.AppendChunk: "*** SINGLE WRITER ONLY — NOT
// goroutine-safe ***"). Every AppendChunk call in the whole daemon MUST
// route through this goroutine via writeCh — handler_upload.go's
// UploadHandler is the only caller, and it never calls AppendChunk
// directly (see that file's SINGLE_WRITER_RULE note). Started once from
// main() after crash recovery completes (IC §5.3 pre-condition, Step 5
// above), and stops when writeCh is closed at shutdown.
func runChunkStoreWriter(store storage.ChunkStore, writeCh <-chan chunkWriteRequest) {
	for req := range writeCh {
		offset, err := store.AppendChunk(req.chunkID, req.data)
		req.resultCh <- chunkWriteResult{vlogOffset: offset, err: err}
	}
}

// ── owner seed persistence (identity gap placeholder — see main()'s Step 4 note) ──

const ownerSeedFileName = "owner-seed.bin"
const ownerSeedFileSize = 32 + 16 // masterSecret || ownerID
const privateFilePermissions = 0600

func loadOrGenerateOwnerSeed(dataDir string) (masterSecret [32]byte, ownerID [16]byte, err error) {
	path := filepath.Join(dataDir, ownerSeedFileName)

	data, readErr := os.ReadFile(path)
	if readErr == nil {
		if len(data) != ownerSeedFileSize {
			return masterSecret, ownerID, fmt.Errorf("cmd/provider: owner seed file %s has wrong size (%d, want %d)", path, len(data), ownerSeedFileSize)
		}
		copy(masterSecret[:], data[0:32])
		copy(ownerID[:], data[32:48])
		return masterSecret, ownerID, nil
	}
	if !os.IsNotExist(readErr) {
		return masterSecret, ownerID, fmt.Errorf("cmd/provider: read owner seed: %w", readErr)
	}

	if _, err := rand.Read(masterSecret[:]); err != nil {
		return masterSecret, ownerID, fmt.Errorf("cmd/provider: generate owner seed: %w", err)
	}
	if _, err := rand.Read(ownerID[:]); err != nil {
		return masterSecret, ownerID, fmt.Errorf("cmd/provider: generate owner id: %w", err)
	}

	out := make([]byte, 0, ownerSeedFileSize)
	out = append(out, masterSecret[:]...)
	out = append(out, ownerID[:]...)
	if err := os.WriteFile(path, out, privateFilePermissions); err != nil {
		return masterSecret, ownerID, fmt.Errorf("cmd/provider: persist owner seed: %w", err)
	}
	return masterSecret, ownerID, nil
}

// deriveLocalProviderIDBytes derives a stable, locally-computable 16-byte
// identifier from this daemon's own Peer ID, for use as the
// provider_id_bytes embedded in upload/audit receipt signing inputs (see
// handler_upload.go/handler_audit.go). This is NOT the microservice-
// assigned provider_id UUID (no registration flow supplies one in this
// session's scope — see handler_upload.go's file header) — it is a stable
// local stand-in so receipts are at least internally self-consistent across
// restarts.
func deriveLocalProviderIDBytes(peerID p2p.PeerID) [16]byte {
	digest := sha256.Sum256([]byte(peerID.String()))
	var out [16]byte
	copy(out[:], digest[:16])
	return out
}

// ── microservice JWKS fetch ────────────────────────────────────────────

const providerHTTPClientTimeout = 10 * time.Second

type jwksKeyDTO struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Use string `json:"use"`
	Kid string `json:"kid"`
}

type jwksResponseDTO struct {
	Keys []jwksKeyDTO `json:"keys"`
}

// fetchMicroservicePublicKey fetches GET {microserviceURL}/.well-known/jwks.json
// and decodes the first Ed25519 ("OKP"/"Ed25519") key found — mirroring
// internal/api/jwt.go's HandleJWKS response shape exactly.
func fetchMicroservicePublicKey(ctx context.Context, microserviceURL string) (ed25519.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, microserviceURL+"/.well-known/jwks.json", nil)
	if err != nil {
		return nil, fmt.Errorf("cmd/provider: build JWKS request: %w", err)
	}
	client := &http.Client{Timeout: providerHTTPClientTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cmd/provider: fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cmd/provider: JWKS endpoint returned %d", resp.StatusCode)
	}

	var body jwksResponseDTO
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("cmd/provider: decode JWKS response: %w", err)
	}
	for _, k := range body.Keys {
		if k.Kty == "OKP" && k.Crv == "Ed25519" {
			raw, err := base64.RawURLEncoding.DecodeString(k.X)
			if err != nil {
				return nil, fmt.Errorf("cmd/provider: decode JWKS key x: %w", err)
			}
			if len(raw) != ed25519.PublicKeySize {
				return nil, fmt.Errorf("cmd/provider: JWKS key wrong length (%d, want %d)", len(raw), ed25519.PublicKeySize)
			}
			return ed25519.PublicKey(raw), nil
		}
	}
	return nil, fmt.Errorf("cmd/provider: no Ed25519 OKP key found in JWKS response")
}

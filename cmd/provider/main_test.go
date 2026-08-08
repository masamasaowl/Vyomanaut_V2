package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/masamasaowl/Vyomanaut_V2/internal/p2p"
)

// TestProviderStartup is a thin wrapper so `go test -run TestProviderStartup`
// (this session's own VERIFY invocation) matches — the three tests it runs
// as subtests are the actual, independently-meaningful test functions below
// (also runnable directly; nothing here is duplicated logic, just
// t.Run(name, existingTestFunc)). FLAGGED: build.md's own VERIFY block
// names three tests whose names do not contain "ProviderStartup" as a
// substring of "TestStartupRunsRAMCheckBeforeChunkStore" etc., so a bare
// `-run TestProviderStartup` would not match any of them without this
// wrapper — the same class of small, independently-recomputed inconsistency
// as the RAM-formula sanity numbers (see internal/storage/ram_requirement.go).
func TestProviderStartup(t *testing.T) {
	t.Run("TestStartupRunsRAMCheckBeforeChunkStore", TestStartupRunsRAMCheckBeforeChunkStore)
	t.Run("TestStartupRecoversBeforeWriterGoroutine", TestStartupRecoversBeforeWriterGoroutine)
	t.Run("TestStartupRegistersAllFourProtocolHandlers", TestStartupRegistersAllFourProtocolHandlers)
}

// TestStartupRunsRAMCheckBeforeChunkStore verifies runRAMCheck (main()'s
// Step 3) has no dependency on a storage.ChunkStore at all — its signature
// takes only a declared-storage value and internally calls
// storage.RequiredDHTCacheRAMMB/storage.AvailableRAMBytes, neither of which
// touches an opened store. This is the structural guarantee that lets it
// run before ChunkStore is ever constructed in main(): there is nothing in
// runRAMCheck that COULD require store state to exist first.
func TestStartupRunsRAMCheckBeforeChunkStore(t *testing.T) {
	// No dataDir, no storage.NewChunkStore call anywhere in this test —
	// deliberately, to demonstrate the independence.
	effectiveGB, constrained := runRAMCheck(50)
	if effectiveGB <= 0 {
		t.Fatalf("runRAMCheck(50) returned effectiveGB = %d, want > 0", effectiveGB)
	}
	// constrained may legitimately be true or false depending on the host
	// running this test; the property under test is that the call
	// completed at all without any store having been opened.
	_ = constrained
}

// TestStartupRecoversBeforeWriterGoroutine verifies the correct sequence —
// RecoverFromCrash completing before the writer goroutine starts consuming
// writes — actually works: a fresh store recovers cleanly, and only after
// that does starting the writer goroutine and submitting a write succeed.
func TestStartupRecoversBeforeWriterGoroutine(t *testing.T) {
	store := newTestChunkStore(t) // this helper already calls RecoverFromCrash (handler_upload_test.go)

	writeCh := make(chan chunkWriteRequest, 1)
	go runChunkStoreWriter(store, writeCh)
	defer close(writeCh)

	data := make([]byte, uploadChunkDataSize)
	_, _ = rand.Read(data)
	chunkID := sha256.Sum256(data)

	resultCh := make(chan chunkWriteResult, 1)
	writeCh <- chunkWriteRequest{chunkID: chunkID, data: data, resultCh: resultCh}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("AppendChunk after RecoverFromCrash: %v", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for write result")
	}

	if _, err := store.LookupChunk(chunkID); err != nil {
		t.Fatalf("LookupChunk after successful write: %v", err)
	}
}

// TestStartupRegistersAllFourProtocolHandlers wires a Host exactly the way
// main() does at Steps 7-8 (construct handlers, SetStreamHandler for each
// of the four protocols) and verifies every one of the four protocol IDs
// is actually reachable by a client — the same structural guarantee
// ALL_FOUR_HANDLERS_REGISTERED's source-text grep checks, exercised at
// runtime instead.
func TestStartupRegistersAllFourProtocolHandlers(t *testing.T) {
	store := newTestChunkStore(t)
	writeCh := startTestChunkWriter(t, store)

	_, providerPriv, _ := ed25519.GenerateKey(rand.Reader)
	msPub, _, _ := ed25519.GenerateKey(rand.Reader)
	statusHolder := newProviderStatusHolder(providerStatusActive)
	authz := newStaticMicroserviceAuthorizer()
	msPeerID := p2p.PeerID("test-microservice-peer")

	uploadHandler := NewUploadHandler(store, writeCh, msPub, providerPriv, [16]byte{}, statusHolder)
	auditHandler := NewAuditHandler(store, providerPriv, [16]byte{})
	repairHandler := NewRepairDownloadHandler(store, msPub, authz, 120*time.Second, msPeerID)
	vettingGCHandler := NewVettingGCHandler(store, msPub, authz, 120*time.Second, msPeerID)

	port := pickFreeLoopbackPort(t)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", port)
	_, serverPriv, _ := ed25519.GenerateKey(rand.Reader)
	serverHost, err := p2p.NewHost(p2p.HostConfig{PrivateKey: serverPriv, ListenAddr: listenAddr})
	if err != nil {
		t.Fatalf("NewHost server: %v", err)
	}
	t.Cleanup(func() { _ = serverHost.Close() })

	serverHost.SetStreamHandler(chunkUploadProtocolID, uploadHandler.HandleStream)
	serverHost.SetStreamHandler(auditChallengeProtocolID, auditHandler.HandleStream)
	serverHost.SetStreamHandler(repairDownloadProtocolID, repairHandler.HandleStream)
	serverHost.SetStreamHandler(vettingGCProtocolID, vettingGCHandler.HandleStream)

	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientHost, err := p2p.NewHost(p2p.HostConfig{PrivateKey: clientPriv})
	if err != nil {
		t.Fatalf("NewHost client: %v", err)
	}
	t.Cleanup(func() { _ = clientHost.Close() })

	ma, err := p2p.ParseMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port))
	if err != nil {
		t.Fatalf("ParseMultiaddr: %v", err)
	}
	ctx := context.Background()
	if err := clientHost.Connect(ctx, serverHost.PeerID(), []p2p.Multiaddr{ma}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	protocols := []p2p.ProtocolID{
		chunkUploadProtocolID,
		auditChallengeProtocolID,
		repairDownloadProtocolID,
		vettingGCProtocolID,
	}
	for _, proto := range protocols {
		stream, err := clientHost.NewStream(ctx, serverHost.PeerID(), proto)
		if err != nil {
			t.Errorf("NewStream(%s): %v", proto, err)
			continue
		}
		_ = stream.Close()
	}
}

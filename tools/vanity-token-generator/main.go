package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/tyler-smith/go-bip32"
	"github.com/tyler-smith/go-bip39"
)

// NetworkType represents different Spark networks - maps to Network
type NetworkType int

const (
	MainnetType NetworkType = iota
	RegtestType
)

// PatternPosition represents where to look for patterns
type PatternPosition int

const (
	Beginning PatternPosition = iota
	End
	Anywhere
)

// Network prefix mapping for bech32m token identifiers
var tokenIdentifierNetworkPrefix = map[NetworkType]string{
	MainnetType: "btkn",
	RegtestType: "btknrt",
}

// CLI flags and default values
var (
	tokenName   = flag.String("name", "", "Token name (3-20 bytes)")
	tokenTicker = flag.String("ticker", "", "Token ticker/symbol (3-6 bytes)")
	decimals    = flag.Uint("decimals", 0, "Number of decimal places (0-18)")
	isFreezable = flag.Bool("freezable", false, "Whether the token can be frozen")
	maxSupply   = flag.String("max-supply", "", "Maximum token supply (will be multiplied by 10^decimals)")
	pattern     = flag.String("pattern", "", "Bech32m pattern to search for in encoded identifier (excluding prefix)")
	position    = flag.String("position", "end", "Where to look for pattern: beginning, end, anywhere")
	maxAttempts = flag.Uint("max-attempts", 1000000, "Maximum number of attempts before giving up")
	networkFlag = flag.String("network", "mainnet", "Network to use: mainnet, regtest")
	threads     = flag.Int("threads", 0, "Number of worker threads (0 = auto-detect based on CPU cores)")
	help        = flag.Bool("help", false, "Show help message")
)

// bech32Alphabet contains the valid characters for bech32/bech32m encoding
const bech32Alphabet = "acdefghjklmnpqrstuvwxyz023456789"

// productionSparkCreationEntityPublicKeyHex is the hex-encoded public key for the Spark layer creation entity.
const productionSparkCreationEntityPublicKeyHex = "0205fe807e8fe1f368df955cc291f16d840b7f28374b0ed80b80c3e2e0921a0674"

// sparkCreationEntityPublicKey is the decoded public key for the Spark layer creation entity.
var sparkCreationEntityPublicKey = mustParsePublicKeyHex(productionSparkCreationEntityPublicKeyHex)

func mustParsePublicKeyHex(hexKey string) keys.Public {
	key, err := keys.ParsePublicKeyHex(hexKey)
	if err != nil {
		panic(fmt.Sprintf("invalid hardcoded creation entity public key: %v", err))
	}
	return key
}

// validateBech32mPattern validates that a pattern contains only valid bech32m characters
func validateBech32mPattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("pattern cannot be empty")
	}

	for i, char := range strings.ToLower(pattern) {
		if !strings.ContainsRune(bech32Alphabet, char) {
			return fmt.Errorf("invalid character '%c' at position %d in pattern '%s'. Valid bech32m characters are: %s",
				char, i, pattern, bech32Alphabet)
		}
	}
	return nil
}

// validateTokenParameters validates the token parameters according to Spark requirements
func validateTokenParameters(name, ticker string, decimals uint, freezable bool, maxSupplyStr string) error {
	_ = freezable // freezable is a boolean that doesn't need validation
	// Validate token name
	if name == "" {
		return fmt.Errorf("token name cannot be empty")
	}
	if len(name) < 3 || len(name) > 20 {
		return fmt.Errorf("token name must be between 3 and 20 bytes, got %d", len(name))
	}

	// Validate token ticker
	if ticker == "" {
		return fmt.Errorf("token ticker cannot be empty")
	}
	if len(ticker) < 3 || len(ticker) > 6 {
		return fmt.Errorf("token ticker must be between 3 and 6 bytes, got %d", len(ticker))
	}

	// Validate decimals (reasonable range)
	if decimals > 18 {
		return fmt.Errorf("decimals must be between 0 and 18, got %d", decimals)
	}

	// Validate max supply
	if _, err := parseMaxSupply(maxSupplyStr); err != nil {
		return fmt.Errorf("invalid max supply: %w", err)
	}

	return nil
}

// parsePosition converts position string to PatternPosition enum
func parsePosition(pos string) (PatternPosition, error) {
	switch strings.ToLower(pos) {
	case "beginning", "start":
		return Beginning, nil
	case "end":
		return End, nil
	case "anywhere", "any":
		return Anywhere, nil
	default:
		return Beginning, fmt.Errorf("invalid position: %s (valid: beginning, end, anywhere)", pos)
	}
}

// parseMaxSupply converts max supply string to bytes, considering decimals
func parseMaxSupply(maxSupplyStr string) ([]byte, error) {
	// Parse the max supply as a big integer
	maxSupplyBase, success := new(big.Int).SetString(maxSupplyStr, 10)
	if !success {
		return nil, fmt.Errorf("invalid max supply format: %s", maxSupplyStr)
	}

	if maxSupplyBase.Sign() < 0 {
		return nil, fmt.Errorf("max supply must be positive, got %s", maxSupplyStr)
	}

	// Convert to 16-byte array (big-endian)
	maxSupplyBytes := make([]byte, 16)
	maxSupplyBase.FillBytes(maxSupplyBytes)

	return maxSupplyBytes, nil
}

// parseNetwork converts network string to NetworkType enum
func parseNetwork(network string) (NetworkType, error) {
	switch strings.ToLower(network) {
	case "mainnet", "main":
		return MainnetType, nil
	case "regtest", "reg":
		return RegtestType, nil
	default:
		return MainnetType, fmt.Errorf("invalid network: %s (valid: mainnet, regtest)", network)
	}
}

// encodeBech32mTokenIdentifier encodes a raw token identifier to bech32m format
func encodeBech32mTokenIdentifier(tokenIdentifier []byte, network NetworkType) (string, error) {
	prefix, exists := tokenIdentifierNetworkPrefix[network]
	if !exists {
		return "", fmt.Errorf("unsupported network: %v", network)
	}

	// Convert 8-bit bytes to 5-bit bech32 data
	bech32Data, err := bech32.ConvertBits(tokenIdentifier, 8, 5, true)
	if err != nil {
		return "", fmt.Errorf("failed to convert bits for bech32m encoding: %w", err)
	}

	// Encode using bech32m
	encoded, err := bech32.EncodeM(prefix, bech32Data)
	if err != nil {
		return "", fmt.Errorf("failed to encode bech32m: %w", err)
	}

	return encoded, nil
}

// extractEncodedDataFromBech32m extracts the encoded data part from a bech32m string (excluding prefix)
func extractEncodedDataFromBech32m(bech32mStr string, network NetworkType) (string, error) {
	prefix, exists := tokenIdentifierNetworkPrefix[network]
	if !exists {
		return "", fmt.Errorf("unsupported network: %v", network)
	}

	expectedPrefix := prefix + "1"
	if !strings.HasPrefix(bech32mStr, expectedPrefix) {
		return "", fmt.Errorf("bech32m string does not have expected prefix %s", expectedPrefix)
	}

	// Extract the encoded data part (everything after the prefix + "1")
	encodedData := bech32mStr[len(expectedPrefix):]
	return encodedData, nil
}

// showUsage displays detailed usage information
func showUsage() {
	fmt.Println("Spark Vanity Token Identifier Generator")
	fmt.Println("======================================")
	fmt.Println("Generates vanity token identifiers using derived issuer public keys")
	fmt.Println("Searches for patterns in the bech32m encoded token identifier (excluding prefix)")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Printf("  %s [flags]\n", os.Args[0])
	fmt.Println()
	fmt.Println("Flags:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Printf("  %s -name=MyToken -ticker=MTK -pattern=qpzr -position=beginning\n", os.Args[0])
	fmt.Printf("  %s -name=CoolCoin -ticker=COOL -decimals=6 -freezable=true -pattern=x8gf -threads=8\n", os.Args[0])
	fmt.Printf("  %s -name=RareToken -ticker=RARE -max-supply=1000000 -decimals=18\n", os.Args[0])
	fmt.Printf("  %s -pattern=w0s3 -position=beginning -network=regtest -threads=16\n", os.Args[0])
	fmt.Println()
	fmt.Println("Note: Pattern matching is done on the bech32m encoded format excluding the network prefix.")
	fmt.Println("Pattern must only contain valid bech32m characters:", bech32Alphabet)
	fmt.Println("For example, 'btkn1w0s3wtn6q4sm...' would match pattern 'w0s3' at the beginning.")
	fmt.Println()
	fmt.Println("Performance: Use -threads to control parallelism. More threads = faster search.")
	fmt.Printf("Auto-detects %d CPU cores. Consider using -threads=%d for maximum performance.\n", runtime.NumCPU(), runtime.NumCPU()*2)
	fmt.Println()
}

// getAccountNumber returns the account number based on network type
// This matches the logic in the TypeScript SparkWallet
func getAccountNumber(network NetworkType) uint32 {
	if network == MainnetType {
		return 1
	}
	return 0 // For regtest
}

// networkTypeToNetwork converts our NetworkType to Network.
func networkTypeToNetwork(networkType NetworkType) btcnetwork.Network {
	switch networkType {
	case MainnetType:
		return btcnetwork.Mainnet
	case RegtestType:
		return btcnetwork.Regtest
	default:
		return btcnetwork.Regtest
	}
}

// generateRandomMnemonic creates a new random BIP39 mnemonic
func generateRandomMnemonic() (string, error) {
	// Generate 128 bits of entropy (12 words) to match TypeScript implementation
	entropy := make([]byte, 16)
	_, err := rand.Read(entropy)
	if err != nil {
		return "", fmt.Errorf("failed to generate entropy: %w", err)
	}

	// Create mnemonic from entropy
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return "", fmt.Errorf("failed to create mnemonic: %w", err)
	}

	return mnemonic, nil
}

// deriveIdentityPublicKey derives the identity public key from a mnemonic
func deriveIdentityPublicKey(mnemonic string, network NetworkType) ([]byte, error) {
	// Validate mnemonic
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, fmt.Errorf("invalid mnemonic")
	}

	// Convert mnemonic to seed (using empty passphrase)
	seed := bip39.NewSeed(mnemonic, "")

	// Create master key from seed
	masterKey, err := bip32.NewMasterKey(seed)
	if err != nil {
		return nil, fmt.Errorf("failed to create master key: %w", err)
	}

	// Get the account number based on network
	accountNumber := getAccountNumber(network)

	// Derive using the EXACT path from TypeScript: m/8797555'/${accountNumber}'/0'
	// First derive m/8797555'
	key8797555, err := masterKey.NewChildKey(8797555 + 0x80000000) // Hardened derivation
	if err != nil {
		return nil, fmt.Errorf("failed to derive m/8797555': %w", err)
	}

	// Then derive m/8797555'/${accountNumber}'
	accountKey, err := key8797555.NewChildKey(accountNumber + 0x80000000) // Hardened derivation
	if err != nil {
		return nil, fmt.Errorf("failed to derive account key: %w", err)
	}

	// Finally derive m/8797555'/${accountNumber}'/0'
	identityKey, err := accountKey.NewChildKey(0 + 0x80000000) // Hardened derivation
	if err != nil {
		return nil, fmt.Errorf("failed to derive identity key: %w", err)
	}

	// Return public key as bytes
	return identityKey.PublicKey().Key, nil
}

// generateTokenIdentifier creates a TokenMetadata with the given issuer public key and computes its identifier
func generateTokenIdentifier(issuerPublicKey []byte, network NetworkType, tokenName, tokenTicker string, decimals uint8, freezable bool, maxSupplyBytes []byte) (TokenIdentifierResult, error) {
	commonNetwork := networkTypeToNetwork(network)
	issuerPubKey, err := keys.ParsePublicKey(issuerPublicKey)
	if err != nil {
		return TokenIdentifierResult{}, fmt.Errorf("failed to parse issuer public key: %w", err)
	}

	// Create TokenMetadata with provided values
	tokenMetadata := &common.TokenMetadata{
		IssuerPublicKey:         issuerPubKey,
		TokenName:               tokenName,
		TokenTicker:             tokenTicker,
		Decimals:                decimals,
		MaxSupply:               maxSupplyBytes,
		IsFreezable:             freezable,
		CreationEntityPublicKey: sparkCreationEntityPublicKey, // Spark layer creation
		Network:                 commonNetwork,
	}

	// Compute the raw token identifier
	tokenId, err := tokenMetadata.ComputeTokenIdentifierV1()
	if err != nil {
		return TokenIdentifierResult{}, fmt.Errorf("failed to compute token identifier: %w", err)
	}

	// Encode to bech32m format
	bech32mTokenId, err := encodeBech32mTokenIdentifier(tokenId, network)
	if err != nil {
		return TokenIdentifierResult{}, fmt.Errorf("failed to encode token identifier to bech32m: %w", err)
	}

	// Extract the encoded data part (excluding prefix)
	encodedData, err := extractEncodedDataFromBech32m(bech32mTokenId, network)
	if err != nil {
		return TokenIdentifierResult{}, fmt.Errorf("failed to extract encoded data: %w", err)
	}

	return TokenIdentifierResult{
		RawTokenId:     tokenId,
		Bech32mTokenId: bech32mTokenId,
		EncodedData:    encodedData,
	}, nil
}

// formatMaxSupply formats the max supply for display, showing both base units and actual token units
func formatMaxSupply(maxSupplyStr string, decimals uint) string {
	// Parse the max supply as a big integer
	_, success := new(big.Int).SetString(maxSupplyStr, 10)
	if !success {
		return maxSupplyStr // fallback to original string
	}

	if decimals > 0 {
		return fmt.Sprintf("%s tokens", maxSupplyStr)
	} else {
		return fmt.Sprintf("%s tokens", maxSupplyStr)
	}
}

// lookForPattern searches for specific patterns in the encoded data (bech32m without prefix)
func lookForPattern(encodedData string, patterns []string, position PatternPosition) bool {
	encodedDataLower := strings.ToLower(encodedData)

	for _, pattern := range patterns {
		patternLower := strings.ToLower(pattern)

		switch position {
		case Beginning:
			if strings.HasPrefix(encodedDataLower, patternLower) {
				return true
			}
		case End:
			if strings.HasSuffix(encodedDataLower, patternLower) {
				return true
			}
		case Anywhere:
			if strings.Contains(encodedDataLower, patternLower) {
				return true
			}
		}
	}
	return false
}

// VanityResult represents a successful vanity pattern match
type VanityResult struct {
	Mnemonic        string
	IssuerPublicKey []byte
	RawTokenId      []byte
	Bech32mTokenId  string
	EncodedData     string
	AttemptNumber   uint64
	WorkerID        int
}

// TokenIdentifierResult represents the result of generating a token identifier
type TokenIdentifierResult struct {
	RawTokenId     []byte
	Bech32mTokenId string
	EncodedData    string
}

// WorkerStats tracks statistics for a worker goroutine
type WorkerStats struct {
	WorkerID      int
	AttemptsCount uint64
	LastBech32m   string
	LastEncoded   string
}

// searchWithWorkers performs multi-threaded vanity token identifier search
func searchWithWorkers(ctx context.Context, numWorkers int, network NetworkType, patterns []string, patternPosition PatternPosition, maxAttempts uint, tokenParams TokenParams) (*VanityResult, error) {
	// Channels for coordination
	resultChan := make(chan *VanityResult, 1)
	statsChan := make(chan WorkerStats, numWorkers)

	// Shared counters
	var totalAttempts uint64
	var wg sync.WaitGroup

	// Start worker goroutines
	for workerID := 0; workerID < numWorkers; workerID++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			var workerAttempts uint64
			var lastBech32m, lastEncoded string

			for {
				// Check if context is cancelled or max attempts reached
				select {
				case <-ctx.Done():
					return
				default:
				}

				currentTotal := atomic.LoadUint64(&totalAttempts)
				if currentTotal >= uint64(maxAttempts) {
					return
				}

				workerAttempts++
				atomic.AddUint64(&totalAttempts, 1)

				// Generate random mnemonic
				mnemonic, err := generateRandomMnemonic()
				if err != nil {
					continue // Skip this attempt on error
				}

				// Derive identity public key (this will be the issuer public key)
				issuerPublicKey, err := deriveIdentityPublicKey(mnemonic, network)
				if err != nil {
					continue // Skip this attempt on error
				}

				// Generate token identifier using the derived issuer public key
				tokenResult, err := generateTokenIdentifier(
					issuerPublicKey, network, tokenParams.Name, tokenParams.Ticker,
					tokenParams.Decimals, tokenParams.Freezable, tokenParams.MaxSupplyBytes)
				if err != nil {
					continue // Skip this attempt on error
				}

				lastBech32m = tokenResult.Bech32mTokenId
				lastEncoded = tokenResult.EncodedData

				// Check for pattern match
				if lookForPattern(tokenResult.EncodedData, patterns, patternPosition) {
					result := &VanityResult{
						Mnemonic:        mnemonic,
						IssuerPublicKey: issuerPublicKey,
						RawTokenId:      tokenResult.RawTokenId,
						Bech32mTokenId:  tokenResult.Bech32mTokenId,
						EncodedData:     tokenResult.EncodedData,
						AttemptNumber:   atomic.LoadUint64(&totalAttempts),
						WorkerID:        workerID,
					}

					// Try to send result (non-blocking in case another worker already found one)
					select {
					case resultChan <- result:
					case <-ctx.Done():
					default:
					}
					return
				}

				// Send periodic stats updates
				if workerAttempts%1000 == 0 {
					select {
					case statsChan <- WorkerStats{
						WorkerID:      workerID,
						AttemptsCount: workerAttempts,
						LastBech32m:   lastBech32m,
						LastEncoded:   lastEncoded,
					}:
					default: // Don't block if channel is full
					}
				}
			}
		}(workerID)
	}

	// Progress reporting goroutine
	progressTicker := time.NewTicker(5 * time.Second)
	defer progressTicker.Stop()

	go func() {
		workerStats := make(map[int]*WorkerStats)

		for {
			select {
			case <-ctx.Done():
				return
			case stats := <-statsChan:
				workerStats[stats.WorkerID] = &stats
			case <-progressTicker.C:
				total := atomic.LoadUint64(&totalAttempts)
				if len(workerStats) > 0 {
					// Show a sample from one of the workers
					for _, stats := range workerStats {
						if stats.LastBech32m != "" {
							fmt.Printf("   [%d/%d] Workers: %d, Sample: %s... Encoded: %s... (searching...)\n",
								total, maxAttempts, numWorkers, stats.LastBech32m[:min(20, len(stats.LastBech32m))],
								stats.LastEncoded[:min(16, len(stats.LastEncoded))])
							break
						}
					}
				} else {
					fmt.Printf("   [%d/%d] Workers: %d (searching...)\n", total, maxAttempts, numWorkers)
				}
			}
		}
	}()

	// Wait for either a result or all workers to finish
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Wait for result or timeout/cancellation
	select {
	case result := <-resultChan:
		if result != nil {
			return result, nil
		}
		return nil, fmt.Errorf("no matches found in %d attempts", atomic.LoadUint64(&totalAttempts))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TokenParams holds token configuration parameters
type TokenParams struct {
	Name           string
	Ticker         string
	Decimals       uint8
	Freezable      bool
	MaxSupplyBytes []byte
}

func main() {
	// Parse command line flags
	flag.Parse()

	// Show help if requested
	if *help {
		showUsage()
		return
	}

	// Validate token parameters
	if err := validateTokenParameters(*tokenName, *tokenTicker, *decimals, *isFreezable, *maxSupply); err != nil {
		fmt.Printf("❌ Invalid token parameters: %v\n", err)
		fmt.Println("\nUse -help for usage information.")
		os.Exit(1)
	}

	// Validate pattern
	if err := validateBech32mPattern(*pattern); err != nil {
		fmt.Printf("❌ Invalid pattern: %v\n", err)
		fmt.Println("\nUse -help for usage information.")
		os.Exit(1)
	}

	// Parse position
	patternPosition, err := parsePosition(*position)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	// Parse network
	network, err := parseNetwork(*networkFlag)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	var positionDescription string
	switch patternPosition {
	case Beginning:
		positionDescription = "beginning"
	case End:
		positionDescription = "end"
	case Anywhere:
		positionDescription = "anywhere"
	}

	fmt.Println("Spark Vanity Token Identifier Generator")
	fmt.Println("======================================")
	fmt.Println("Generates vanity token identifiers using derived issuer public keys")
	fmt.Println("Searches for patterns in the bech32m encoded token identifier (excluding prefix)")
	fmt.Println()

	fmt.Printf("Token Parameters:\n")
	fmt.Printf("  Name: %s\n", *tokenName)
	fmt.Printf("  Ticker: %s\n", *tokenTicker)
	fmt.Printf("  Decimals: %d\n", *decimals)
	fmt.Printf("  Max Supply: %s\n", formatMaxSupply(*maxSupply, *decimals))
	fmt.Printf("  Freezable: %t\n", *isFreezable)
	fmt.Printf("  Creation Layer: Spark\n")
	fmt.Printf("  Network: %s (prefix: %s)\n", *networkFlag, tokenIdentifierNetworkPrefix[network])
	fmt.Println()
	fmt.Printf("Search Parameters:\n")
	fmt.Printf("  Pattern: %s\n", *pattern)
	fmt.Printf("  Position: %s\n", positionDescription)
	fmt.Printf("  Max Attempts: %d\n", *maxAttempts)
	fmt.Printf("  Derivation Path: m/8797555'/%d'/0'\n", getAccountNumber(network))
	fmt.Println("Press Ctrl+C to stop...")
	fmt.Println()

	patterns := []string{*pattern}

	// Determine number of workers
	numWorkers := *threads
	if numWorkers == 0 {
		numWorkers = runtime.NumCPU()
	}

	fmt.Printf("Performance:\n")
	fmt.Printf("  Workers: %d threads", numWorkers)
	if *threads == 0 {
		fmt.Printf(" (auto-detected from %d CPU cores)", runtime.NumCPU())
	}
	fmt.Printf("\n")
	fmt.Printf("  Expected throughput: ~%d attempts/second per thread\n", 500) // Rough estimate
	fmt.Println()

	// Parse max supply
	maxSupplyBytes, err := parseMaxSupply(*maxSupply)
	if err != nil {
		fmt.Printf("❌ Error parsing max supply: %v\n", err)
		os.Exit(1)
	}

	// Create a context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	accountNumber := getAccountNumber(network)
	fmt.Printf("=== Searching %s Network (account number: %d, path: m/8797555'/%d'/0') ===\n",
		*networkFlag, accountNumber, accountNumber)

	// Start the search with workers
	result, err := searchWithWorkers(ctx, numWorkers, network, patterns, patternPosition, *maxAttempts, TokenParams{
		Name:           *tokenName,
		Ticker:         *tokenTicker,
		Decimals:       uint8(*decimals),
		Freezable:      *isFreezable,
		MaxSupplyBytes: maxSupplyBytes,
	})

	if err != nil {
		log.Printf("Search failed: %v", err)
		fmt.Printf("❌ Search failed: %v\n", err)
		os.Exit(1)
	}

	if result != nil {
		fmt.Printf("\n🎯 FOUND VANITY TOKEN IDENTIFIER! (pattern '%s' at %s after %d attempts):\n",
			*pattern, positionDescription, result.AttemptNumber)
		fmt.Printf("   Mnemonic: %s\n", result.Mnemonic)
		fmt.Printf("   Issuer Public Key: %s\n", hex.EncodeToString(result.IssuerPublicKey))
		fmt.Printf("   Raw Token Identifier: %s\n", hex.EncodeToString(result.RawTokenId))
		fmt.Printf("   Bech32m Token Identifier: %s\n", result.Bech32mTokenId)
		fmt.Printf("   Encoded Data (searched): %s\n", result.EncodedData)
		fmt.Printf("   Token Name: %s\n", *tokenName)
		fmt.Printf("   Token Ticker: %s\n", *tokenTicker)
		fmt.Printf("   Decimals: %d\n", *decimals)
		fmt.Printf("   Max Supply: %s\n", formatMaxSupply(*maxSupply, *decimals))
		fmt.Printf("   Freezable: %t\n", *isFreezable)
		fmt.Printf("   Network: %s (path: m/8797555'/%d'/0')\n", *networkFlag, accountNumber)
		fmt.Println("\n✅ Vanity token identifier search completed successfully!")
	} else {
		fmt.Printf("❌ No matches found in %d attempts for %s network\n", *maxAttempts, *networkFlag)
		fmt.Println("\n=== Search Complete ===")
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	testutil "github.com/lightsparkdev/spark-go/test_util"
	"github.com/lightsparkdev/spark-go/wallet"
	"github.com/tyler-smith/go-bip32"
)

// WalletStatus represents the state of the wallet
type WalletStatus struct {
	Initialized bool
	// Add more wallet-specific fields here
}

// Command represents a CLI command and its handler function
type Command struct {
	Name        string
	Description string
	Usage       string
	Handler     func(args []string) error
}

// CommandRegistry manages the available commands
type CommandRegistry struct {
	commands map[string]Command
}

// NewCommandRegistry creates a new command registry
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		commands: make(map[string]Command),
	}
}

// RegisterCommand adds a new command to the registry
func (r *CommandRegistry) RegisterCommand(cmd Command) {
	r.commands[strings.ToLower(cmd.Name)] = cmd
}

// GetCommand retrieves a command from the registry
func (r *CommandRegistry) GetCommand(name string) (Command, bool) {
	cmd, exists := r.commands[strings.ToLower(name)]
	return cmd, exists
}

// ListCommands returns a list of all available commands and their descriptions
func (r *CommandRegistry) ListCommands() []Command {
	var cmdList []Command
	for _, cmd := range r.commands {
		cmdList = append(cmdList, cmd)
	}
	return cmdList
}

// CLI represents the command-line interface
type CLI struct {
	registry *CommandRegistry
	reader   *bufio.Reader
	wallet   *wallet.SingleKeyWallet
}

// NewCLI creates a new CLI instance
func NewCLI() *CLI {
	return &CLI{
		registry: NewCommandRegistry(),
		reader:   bufio.NewReader(os.Stdin),
	}
}

// parseInput splits the input into command and arguments
func parseInput(input string) (string, []string) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return "", nil
	}

	command := strings.ToLower(parts[0])
	var args []string
	if len(parts) > 1 {
		args = parts[1:]
	}

	return command, args
}

// InitializeWallet handles the wallet setup process
func (cli *CLI) InitializeWallet() ([]byte, error) {
	fmt.Println("Welcome to the Spark Wallet CLI!")
	fmt.Println("Please enter your secret seed:")

	for {
		fmt.Print("Enter your secret seed: ")
		input, err := cli.reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("error reading input: %w", err)
		}

		seed, err := hex.DecodeString(strings.TrimSpace(input))
		if err != nil {
			fmt.Println("Invalid seed. Please enter a valid hex string.")
			continue
		}

		return seed, nil
	}
}

// Run starts the CLI loop
func (cli *CLI) Run() error {
	seed, err := cli.InitializeWallet()
	if err != nil {
		return fmt.Errorf("wallet initialization failed: %w", err)
	}

	masterKey, err := bip32.NewMasterKey(seed)
	if err != nil {
		return fmt.Errorf("failed to create master key: %w", err)
	}

	identityKey, err := masterKey.NewChildKey(0 + 0x80000000)
	if err != nil {
		return fmt.Errorf("failed to create identity key: %w", err)
	}
	fmt.Printf("Identity key pubkey: %s\n", hex.EncodeToString(identityKey.PublicKey().Key))
	signingKey, err := masterKey.NewChildKey(1 + 0x80000000)
	if err != nil {
		return fmt.Errorf("failed to create signing key: %w", err)
	}
	fmt.Printf("Signing key pubkey: %s\n", hex.EncodeToString(signingKey.PublicKey().Key))
	config, err := testutil.TestWalletConfigDeployed(identityKey.Key)
	if err != nil {
		return fmt.Errorf("failed to create test wallet config: %w", err)
	}

	cli.wallet = wallet.NewSingleKeyWallet(config, signingKey.Key)

	fmt.Println("\nWallet initialized. Ready for commands.")

	// Regular command loop
	for {
		fmt.Print("> ")
		input, err := cli.reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("error reading input: %w", err)
		}

		command, args := parseInput(strings.TrimSpace(input))
		if command == "" {
			continue
		}

		if cmd, exists := cli.registry.GetCommand(command); exists {
			if err := cmd.Handler(args); err != nil {
				fmt.Printf("Error executing command: %v\n", err)
			}
		} else {
			fmt.Println("Unknown command. Available commands:")
			for _, cmd := range cli.registry.ListCommands() {
				fmt.Printf("  %s - %s\n", cmd.Usage, cmd.Description)
			}
		}
	}
}

func main() {
	cli := NewCLI()

	// Register commands
	cli.registry.RegisterCommand(Command{
		Name:        "create_invoice",
		Description: "Create an invoice",
		Usage:       "create_invoice <amount> <memo>",
		Handler: func(args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("please provide an amount and memo")
			}
			amount, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid amount: %w", err)
			}
			memo := args[1]
			fmt.Printf("Creating invoice for %d with memo %s\n", amount, memo)
			invoice, fee, err := cli.wallet.CreateLightningInvoice(context.Background(), int64(amount), memo)
			if err != nil {
				return fmt.Errorf("failed to create invoice: %w", err)
			}
			fmt.Printf("Invoice created: %s\n", *invoice)
			fmt.Printf("Fee: %d\n", fee)
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "exit",
		Description: "Exit the program",
		Usage:       "exit",
		Handler: func(_ []string) error {
			fmt.Println("Goodbye!")
			os.Exit(0)
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "claim",
		Description: "Claim transfers",
		Usage:       "claim",
		Handler: func(_ []string) error {
			nodes, err := cli.wallet.ClaimAllTransfers(context.Background())
			if err != nil {
				return fmt.Errorf("failed to claim transfers: %w", err)
			}
			amount := 0
			for _, node := range nodes {
				amount += int(node.Value)
				fmt.Printf("Claimed node %s for %d sats\n", node.Id, node.Value)
			}
			fmt.Printf("Total amount claimed: %d sats\n", amount)
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "pay",
		Description: "Pay an invoice",
		Usage:       "pay <invoice>",
		Handler: func(args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("please provide an invoice")
			}
			invoice := args[0]
			fmt.Printf("Paying invoice: %s\n", invoice)
			requestID, err := cli.wallet.PayInvoice(context.Background(), invoice)
			if err != nil {
				return fmt.Errorf("failed to pay invoice: %w", err)
			}
			fmt.Printf("Invoice paid: %s\n", requestID)
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "sync",
		Description: "Sync wallet",
		Usage:       "sync",
		Handler: func(_ []string) error {
			return cli.wallet.SyncWallet(context.Background())
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "balance",
		Description: "Show balance",
		Usage:       "balance",
		Handler: func(_ []string) error {
			err := cli.wallet.SyncWallet(context.Background())
			if err != nil {
				return fmt.Errorf("failed to sync wallet: %w", err)
			}
			balance := 0
			for _, node := range cli.wallet.OwnedNodes {
				fmt.Printf("Leaf %s: %d sats\n", node.Id, node.Value)
				balance += int(node.Value)
			}
			fmt.Printf("Total balance: %d sats\n", balance)
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "send",
		Description: "Send transfer",
		Usage:       "send <receiver_identity_pubkey> <amount>",
		Handler: func(args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("please provide a receiver identity pubkey in hex string format and amount")
			}
			receiverIdentityPubkey, err := hex.DecodeString(args[0])
			if err != nil {
				return fmt.Errorf("invalid receiver identity pubkey: %w", err)
			}
			amount, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid amount: %w", err)
			}
			transfer, err := cli.wallet.SendTransfer(context.Background(), receiverIdentityPubkey, int64(amount))
			if err != nil {
				return fmt.Errorf("failed to send transfer: %w", err)
			}
			fmt.Printf("Transfer sent: %s\n", transfer.Id)
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "help",
		Description: "Show available commands",
		Usage:       "help",
		Handler: func(_ []string) error {
			fmt.Println("Available commands:")
			for _, cmd := range cli.registry.ListCommands() {
				fmt.Printf("  %s - %s\n", cmd.Usage, cmd.Description)
			}
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "swap",
		Description: "Swap leaves",
		Usage:       "swap <amount>",
		Handler: func(args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("please provide an amount")
			}
			amount, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid amount: %w", err)
			}
			nodes, err := cli.wallet.RequestLeavesSwap(context.Background(), int64(amount))
			if err != nil {
				return fmt.Errorf("failed to request leaves swap: %w", err)
			}
			amountClaimed := 0
			for _, node := range nodes {
				amountClaimed += int(node.Value)
				fmt.Printf("Swapped node %s for %d sats\n", node.Id, node.Value)
			}
			fmt.Printf("Total amount claimed: %d sats\n", amountClaimed)
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "mint_tokens",
		Description: "Mint tokens",
		Usage:       "mint_tokens <amount>",
		Handler: func(args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("please provide an amount")
			}
			amount, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid amount: %w", err)
			}

			fmt.Printf("Minting %d tokens with token public key %s\n", amount, hex.EncodeToString(cli.wallet.Config.IdentityPublicKey()))
			err = cli.wallet.MintTokens(context.Background(), amount)
			if err != nil {
				return fmt.Errorf("failed to mint tokens: %w", err)
			}
			fmt.Printf("%d tokens minted\n", amount)
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "transfer_tokens",
		Description: "Transfer tokens",
		Usage:       "transfer_tokens <amount> <receiver_public_key>",
		Handler: func(args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("please provide an amount and receiver public key")
			}
			amount, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid amount: %w", err)
			}
			receiverPublicKey, err := hex.DecodeString(args[1])
			if err != nil {
				return fmt.Errorf("invalid receiver public key: failed to decode hex string: %w", err)
			}
			if len(receiverPublicKey) != 33 {
				return fmt.Errorf("invalid receiver public key: decoded bytes must be 33 bytes (66 hex characters), got %d bytes from %d hex characters: %s",
					len(receiverPublicKey),
					len(args[1]),
					args[1])
			}
			fmt.Printf("Transferring %d tokens to public key %s\n", amount, args[1])
			err = cli.wallet.TransferTokens(context.Background(), amount, receiverPublicKey)
			if err != nil {
				return fmt.Errorf("failed to transfer tokens: %w", err)
			}
			fmt.Printf("%d tokens transferred with Token Public Key: %s\n", amount, hex.EncodeToString(cli.wallet.Config.IdentityPublicKey()))
			return nil
		},
	})

	cli.registry.RegisterCommand(Command{
		Name:        "token_balance",
		Description: "Get token balance for a specific token public key",
		Usage:       "token_balance",
		Handler: func(args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("please provide a token public key in hex string format")
			}
			tokenPublicKey, err := hex.DecodeString(args[0])
			if err != nil {
				return fmt.Errorf("invalid token public key: failed to decode hex string: %w", err)
			}
			if len(tokenPublicKey) != 33 {
				return fmt.Errorf("invalid token public key: decoded bytes must be 33 bytes (66 hex characters), got %d bytes from %d hex characters: %s",
					len(tokenPublicKey),
					len(args[0]),
					args[0])
			}

			numLeaves, totalAmount, err := cli.wallet.GetTokenBalance(context.Background(), tokenPublicKey)
			if err != nil {
				return fmt.Errorf("failed to get token balance: %w", err)
			}

			fmt.Printf("Token Public Key: %s\n", args[0])
			fmt.Printf("Number of leaves: %d\n", numLeaves)
			fmt.Printf("Total amount: %d tokens\n", totalAmount)
			return nil
		},
	})

	// Start the CLI
	if err := cli.Run(); err != nil {
		fmt.Printf("Error running CLI: %v\n", err)
		os.Exit(1)
	}
}

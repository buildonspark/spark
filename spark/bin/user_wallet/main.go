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
	wallet   *wallet.SignleKeyWallet
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
	signingKey, err := masterKey.NewChildKey(1 + 0x80000000)
	if err != nil {
		return fmt.Errorf("failed to create signing key: %w", err)
	}

	config, err := testutil.TestWalletConfigDeployed(identityKey.Key)
	if err != nil {
		return fmt.Errorf("failed to create test wallet config: %w", err)
	}

	cli.wallet = wallet.NewSignleKeyWallet(config, signingKey.Key)

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

	// Start the CLI
	if err := cli.Run(); err != nil {
		fmt.Printf("Error running CLI: %v\n", err)
		os.Exit(1)
	}
}

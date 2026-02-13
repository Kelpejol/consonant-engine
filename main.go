// Beam CLI - Command-line interface for Beam operations
//
// This tool provides administrative operations for Beam including:
// - Balance management (get, add, deduct)
// - Customer management (create, list, delete)
// - Request tracking (list, show)
// - Admin operations (sync, verify integrity)
//
// Usage:
//   beam-cli balance get --customer-id cus_123
//   beam-cli customers list
//   beam-cli requests list --customer-id cus_123
//   beam-cli admin sync-all
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-redis/redis/v8"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/yourusername/beam/internal/ledger"
	"github.com/yourusername/beam/internal/sync"
)

var (
	// Version is set during build
	Version   = "dev"
	BuildTime = "unknown"

	// Global flags
	redisAddr   string
	postgresURL string
	verbose     bool

	// Ledger instance
	ldgr *ledger.Ledger
)

func main() {
	// Setup logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	// Root command
	rootCmd := &cobra.Command{
		Use:   "beam-cli",
		Short: "Beam CLI - Command-line interface for Beam operations",
		Long: `Beam CLI provides administrative operations for the Beam real-time AI cost enforcement system.
		
Operations include balance management, customer management, request tracking, and admin tools.`,
		Version:       Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Setup logger level
			if verbose {
				zerolog.SetGlobalLevel(zerolog.DebugLevel)
			} else {
				zerolog.SetGlobalLevel(zerolog.InfoLevel)
			}

			// Initialize ledger for commands that need it
			if cmd.Name() != "version" && cmd.Name() != "help" {
				var err error
				ldgr, err = ledger.NewLedger(redisAddr, postgresURL, log.Logger)
				if err != nil {
					return fmt.Errorf("failed to initialize ledger: %w", err)
				}
			}

			return nil
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			if ldgr != nil {
				ldgr.Close()
			}
		},
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&redisAddr, "redis-addr", getEnv("REDIS_ADDR", "localhost:6379"), "Redis address")
	rootCmd.PersistentFlags().StringVar(&postgresURL, "postgres-url", getEnv("POSTGRES_URL", "postgres://postgres:postgres@localhost:5432/beam?sslmode=disable"), "PostgreSQL connection URL")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")

	// Add command groups
	rootCmd.AddCommand(balanceCmd())
	rootCmd.AddCommand(customersCmd())
	rootCmd.AddCommand(requestsCmd())
	rootCmd.AddCommand(adminCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// balanceCmd creates the balance command group
func balanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "balance",
		Short: "Balance operations",
		Long:  "Manage customer balances (get, add, deduct)",
	}

	// balance get
	getCmd := &cobra.Command{
		Use:   "get",
		Short: "Get customer balance",
		RunE: func(cmd *cobra.Command, args []string) error {
			customerID, _ := cmd.Flags().GetString("customer-id")

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			balance, reserved, available, err := ldgr.GetBalance(ctx, customerID)
			if err != nil {
				return fmt.Errorf("failed to get balance: %w", err)
			}

			result := map[string]interface{}{
				"customer_id": customerID,
				"balance":     balance,
				"reserved":    reserved,
				"available":   available,
				"balance_usd": float64(balance) / 1000000,
			}

			printJSON(result)
			return nil
		},
	}
	getCmd.Flags().String("customer-id", "", "Customer ID (required)")
	getCmd.MarkFlagRequired("customer-id")

	// balance add
	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add balance (credit)",
		RunE: func(cmd *cobra.Command, args []string) error {
			customerID, _ := cmd.Flags().GetString("customer-id")
			amount, _ := cmd.Flags().GetInt64("amount")
			description, _ := cmd.Flags().GetString("description")

			// TODO: Implement add balance logic via ledger
			fmt.Printf("Adding %d grains to customer %s\n", amount, customerID)
			fmt.Printf("Description: %s\n", description)
			fmt.Println("Note: Full implementation requires transaction recording")

			return nil
		},
	}
	addCmd.Flags().String("customer-id", "", "Customer ID (required)")
	addCmd.Flags().Int64("amount", 0, "Amount in grains (required)")
	addCmd.Flags().String("description", "CLI credit", "Transaction description")
	addCmd.MarkFlagRequired("customer-id")
	addCmd.MarkFlagRequired("amount")

	cmd.AddCommand(getCmd, addCmd)
	return cmd
}

// customersCmd creates the customers command group
func customersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "customers",
		Short: "Customer management",
		Long:  "Manage customers (list, create, delete)",
	}

	// customers list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all customers",
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, _ := cmd.Flags().GetInt("limit")

			db := ldgr.GetDB()
			rows, err := db.Query(`
				SELECT customer_id, name, current_balance_grains, lifetime_spent_grains, created_at
				FROM customers
				ORDER BY created_at DESC
				LIMIT $1
			`, limit)
			if err != nil {
				return fmt.Errorf("query failed: %w", err)
			}
			defer rows.Close()

			customers := []map[string]interface{}{}
			for rows.Next() {
				var id, name string
				var balance, spent int64
				var created time.Time

				if err := rows.Scan(&id, &name, &balance, &spent, &created); err != nil {
					continue
				}

				customers = append(customers, map[string]interface{}{
					"customer_id":      id,
					"name":             name,
					"balance_grains":   balance,
					"balance_usd":      float64(balance) / 1000000,
					"spent_grains":     spent,
					"spent_usd":        float64(spent) / 1000000,
					"created_at":       created.Format(time.RFC3339),
				})
			}

			printJSON(customers)
			return nil
		},
	}
	listCmd.Flags().Int("limit", 10, "Maximum number of customers to return")

	cmd.AddCommand(listCmd)
	return cmd
}

// requestsCmd creates the requests command group
func requestsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "requests",
		Short: "Request tracking",
		Long:  "View and manage AI requests",
	}

	// requests list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List requests for a customer",
		RunE: func(cmd *cobra.Command, args []string) error {
			customerID, _ := cmd.Flags().GetString("customer-id")
			limit, _ := cmd.Flags().GetInt("limit")

			db := ldgr.GetDB()
			rows, err := db.Query(`
				SELECT request_id, model, status, estimated_cost_grains, actual_cost_grains, 
				       created_at, completed_at
				FROM requests
				WHERE customer_id = $1
				ORDER BY created_at DESC
				LIMIT $2
			`, customerID, limit)
			if err != nil {
				return fmt.Errorf("query failed: %w", err)
			}
			defer rows.Close()

			requests := []map[string]interface{}{}
			for rows.Next() {
				var id, model, status string
				var estimated, actual sql.NullInt64
				var created time.Time
				var completed sql.NullTime

				if err := rows.Scan(&id, &model, &status, &estimated, &actual, &created, &completed); err != nil {
					continue
				}

				req := map[string]interface{}{
					"request_id":         id,
					"model":              model,
					"status":             status,
					"estimated_grains":   estimated.Int64,
					"actual_grains":      actual.Int64,
					"created_at":         created.Format(time.RFC3339),
				}

				if completed.Valid {
					req["completed_at"] = completed.Time.Format(time.RFC3339)
					req["duration_seconds"] = completed.Time.Sub(created).Seconds()
				}

				requests = append(requests, req)
			}

			printJSON(requests)
			return nil
		},
	}
	listCmd.Flags().String("customer-id", "", "Customer ID (required)")
	listCmd.Flags().Int("limit", 10, "Maximum number of requests to return")
	listCmd.MarkFlagRequired("customer-id")

	cmd.AddCommand(listCmd)
	return cmd
}

// adminCmd creates the admin command group
func adminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Administrative operations",
		Long:  "Advanced admin operations (sync, verify, etc.)",
	}

	// admin sync-all
	syncCmd := &cobra.Command{
		Use:   "sync-all",
		Short: "Sync all customer balances from PostgreSQL to Redis",
		RunE: func(cmd *cobra.Command, args []string) error {
			rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
			defer rdb.Close()

			syncer := sync.NewSyncer(rdb, ldgr.GetDB(), log.Logger)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			log.Info().Msg("Starting full sync...")
			if err := syncer.InitializeRedis(ctx); err != nil {
				return fmt.Errorf("sync failed: %w", err)
			}

			log.Info().Msg("✓ Sync complete")
			return nil
		},
	}

	// admin verify-integrity
	verifyCmd := &cobra.Command{
		Use:   "verify-integrity",
		Short: "Verify balance integrity between Redis and PostgreSQL",
		RunE: func(cmd *cobra.Command, args []string) error {
			customerID, _ := cmd.Flags().GetString("customer-id")

			db := ldgr.GetDB()
			var pgBalance, txSum, diff int64
			var valid bool

			err := db.QueryRow(`
				SELECT * FROM verify_balance_integrity($1)
			`, customerID).Scan(&customerID, &pgBalance, &txSum, &diff, &valid)

			if err != nil {
				return fmt.Errorf("verification failed: %w", err)
			}

			result := map[string]interface{}{
				"customer_id":      customerID,
				"postgres_balance": pgBalance,
				"transactions_sum": txSum,
				"difference":       diff,
				"is_valid":         valid,
			}

			printJSON(result)

			if !valid {
				log.Warn().Msg("⚠️  Balance integrity check FAILED")
				return fmt.Errorf("balance mismatch detected")
			}

			log.Info().Msg("✓ Balance integrity verified")
			return nil
		},
	}
	verifyCmd.Flags().String("customer-id", "", "Customer ID (required)")
	verifyCmd.MarkFlagRequired("customer-id")

	cmd.AddCommand(syncCmd, verifyCmd)
	return cmd
}

// Helpers

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func printJSON(v interface{}) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		return
	}
	fmt.Println(string(b))
}
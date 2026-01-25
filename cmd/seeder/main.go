package main

import (
	"database/sql"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	_ "github.com/lib/pq"
)

func main() {
	// Load env vars roughly (or rely on them being exported)
	postgresURL := os.Getenv("POSTGRES_URL")
	if postgresURL == "" {
        // Fallback to reading .env manualy since godotenv isn't here
        data, _ := ioutil.ReadFile(".env")
        lines := strings.Split(string(data), "\n")
        for _, line := range lines {
            if strings.HasPrefix(line, "POSTGRES_URL=") {
                postgresURL = strings.TrimPrefix(line, "POSTGRES_URL=")
                break
            }
        }
	}

    if postgresURL == "" {
        log.Fatal("POSTGRES_URL not found")
    }

	db, err := sql.Open("postgres", postgresURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatal("Ping failed:", err)
	}

	fmt.Println("Connected to DB")

	// 1. Run Migrations
	fmt.Println("Running migrations...")
	migrationFile, err := ioutil.ReadFile("../../migrations/001_initial_schema.up.sql")
	if err != nil {
		// Try local path if running from root
		migrationFile, err = ioutil.ReadFile("migrations/001_initial_schema.up.sql")
		if err != nil {
			log.Fatal("Could not find migration file:", err)
		}
	}

	// Exec the whole migration file at once. lib/pq supports multiple statements in Exec
	_, err = db.Exec(string(migrationFile))
	if err != nil {
		log.Printf("Migration warning (might be already applied): %v\n", err)
	} else {
		fmt.Println("Migrations applied successfully")
	}

	// 2. Run Seed Data
	fmt.Println("Seeding data...")
	sqlFile, err := ioutil.ReadFile("test_seed.sql")
	if err != nil {
		// Try alternate path
		sqlFile, err = ioutil.ReadFile("../../test_seed.sql")
		if err != nil {
			log.Fatal(err)
		}
	}

	// Split by semicolon for seed data (simple inserts)
	requests := strings.Split(string(sqlFile), ";")

	for _, request := range requests {
        request = strings.TrimSpace(request)
        if request == "" {
            continue
        }
		_, err := db.Exec(request)
		if err != nil {
            fmt.Printf("Error executing statement: %v\nStatement: %s\n", err, request)
		}
	}

	fmt.Println("Seeding complete")
}

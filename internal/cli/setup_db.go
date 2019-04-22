package cli

import (
	"github.com/jackc/pgx"
	"github.com/perangel/warp-pipe/internal/db"
	"github.com/spf13/cobra"
)

// Flags
var (
	setupDBIgnoreTables []string
	setupDBSchema       string
)

var setupDBCmd = &cobra.Command{
	Use:   "setup-db",
	Short: "Setup the source database",
	Long: `Setup the source database for tracking changesets.

This command adds a new 'warp_pipe' schema with a 'changesets' table to the source
database, and registers a TRIGGER that will write all table changes after INSERT,
UPDATE, or DELETE to the 'warp_pipe.changesets' table.

Once this is setup, you can run 'warp-pipe' with the 'queue' listener to stream
the changesets.

For more details see: https://github.com/perangel/warp-pipe/docs/setup_database.md
	`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := parseConfig()
		if err != nil {
			return err
		}

		dbConfig := &pgx.ConnConfig{
			Host:     config.ConnConfig.DBHost,
			Port:     uint16(config.ConnConfig.DBPort),
			User:     config.ConnConfig.DBUser,
			Password: config.ConnConfig.DBPass,
			Database: config.ConnConfig.DBName,
		}

		conn, err := pgx.Connect(*dbConfig)
		if err != nil {
			return err
		}

		err = db.SetupDatabase(conn, setupDBSchema, setupDBIgnoreTables)
		if err != nil {
			return err
		}

		return nil
	},
}

func init() {
	setupDBCmd.Flags().StringSliceVarP(&setupDBIgnoreTables, "ignore-tables", "i", nil, "tables to exclude from replication setup")
	setupDBCmd.Flags().StringVarP(&setupDBSchema, "schema", "S", "public", "schema to setup for replication")
}
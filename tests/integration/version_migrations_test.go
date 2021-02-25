package integration

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx"
	"github.com/stretchr/testify/require"

	warppipe "github.com/perangel/warp-pipe"
	"github.com/perangel/warp-pipe/db"
)

var (
	runIntegrationTests = flag.Bool("integration", false, "run integration tests (default false)")
)

type testData struct {
	text    string
	date    time.Time
	boolean bool
	json    []byte
	jsonb   []byte
	array   []int32
}

var (
	dbUser     = "test"
	dbPassword = "test"
	dbName     = "test"
	testSchema = []string{
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE TABLE "testTable" (
			id UUID PRIMARY KEY DEFAULT uuid_generate_v1mc(),
			type_text TEXT,
			type_date DATE,
			type_boolean BOOLEAN,
			type_json JSON,
			type_jsonb JSONB,
			type_array int4[]
		)`,
		`CREATE TABLE empty_table (
			id SERIAL PRIMARY KEY
		)`,
	}
)

func setupTestSchema(config pgx.ConnConfig) error {
	// wait until db is ready to obtain connection
	srcReady := waitForPostgresReady(&config)
	if !srcReady {
		return fmt.Errorf("database did not become ready in allowed time")
	}

	conn, err := pgx.Connect(config)
	if err != nil {
		return err
	}
	defer conn.Close()

	for _, s := range testSchema {
		_, err = conn.Exec(s)
		if err != nil {
			return fmt.Errorf("test schema installation failed: %v", err)
		}
	}
	return nil
}

func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForPostgresReady(config *pgx.ConnConfig) bool {
	connected := false
	for count := 0; count < 30; count++ {
		conn, err := pgx.Connect(*config)
		if err == nil {
			connected = true
			conn.Close()
			break
		}
		time.Sleep(2 * time.Second)
	}
	return connected
}

func createDatabaseContainer(t *testing.T, ctx context.Context, version string, database string, username string, password string) (string, int, error) {
	postgresPort := 5432

	docker, err := NewDockerClient()
	if err != nil {
		return "", 0, err
	}

	hostPort, err := getFreePort()
	if err != nil {
		return "", 0, errors.New("could not determine a free port")
	}

	container, err := docker.runContainer(
		ctx,
		&ContainerConfig{
			image: fmt.Sprintf("postgres:%s", version),
			ports: []*PortMapping{
				{
					HostPort:      fmt.Sprintf("%d", hostPort),
					ContainerPort: fmt.Sprintf("%d", postgresPort),
				},
			},
			env: []string{
				fmt.Sprintf("POSTGRES_DB=%s", database),
				fmt.Sprintf("POSTGRES_USER=%s", username),
				fmt.Sprintf("POSTGRES_PASSWORD=%s", password),
			},
			cmd: []string{
				"postgres",
				"-cwal_level=logical",
				"-cmax_replication_slots=1",
				"-cmax_wal_senders=1",
			},
		})
	if err != nil {
		return "", 0, err
	}

	t.Cleanup(func() {
		if err := docker.removeContainer(ctx, container.ID); err != nil {
			t.Errorf("Could not remove container %s: %v", container.ID, err)
		}
	})

	return container.ID, hostPort, nil
}

func testRows() []testData {
	return []testData{
		// Nil and empty fields.
		{},
		// Empty JSON and slice.
		{
			text:    "text",
			boolean: true,
			date:    time.Now(),
			json:    []byte(`{}`),
			jsonb:   []byte(`{}`),
			array:   make([]int32, 0),
		},
		// Fully populated fields.
		{
			text:    "a test string",
			boolean: true,
			date:    time.Now(),
			json:    []byte(`{"name": "Alice", "age": 31, "city": "LA"}`),
			jsonb:   []byte(`{"name": "Bob", "age": 39, "city": "London"}`),
			array:   []int32{1, 2, 3, 4, 5},
		},
	}
}

func insertTestData(t *testing.T, config pgx.ConnConfig, wg *sync.WaitGroup) {
	defer wg.Done()

	conn, err := pgx.Connect(config)
	if err != nil {
		t.Fatalf("%s: could not connected to source database to insert: %v", t.Name(), err)
		return
	}
	defer conn.Close()

	insertSQL := `
		INSERT INTO
			"testTable" (type_text, type_date, type_boolean, type_json, type_jsonb, type_array)
		VALUES ($1, $2, $3, $4, $5, $6);`

	nRows := 50
	for i := 0; i < nRows; i++ {
		rows := testRows()
		for _, row := range rows {
			_, err = conn.Exec(insertSQL, row.text, row.date, row.boolean, row.json, row.jsonb, row.array)
			if err != nil {
				t.Fatalf("%s: Could not insert row in source database: %v", t.Name(), err)
			}
		}
	}
}

func updateTestData(t *testing.T, config pgx.ConnConfig, wg *sync.WaitGroup) {
	defer wg.Done()

	conn, err := pgx.Connect(config)
	if err != nil {
		t.Fatalf("%s: could not connected to source database to update: %v", t.Name(), err)
		return
	}
	defer conn.Close()

	// update one field in one row
	updateSQL := `
		UPDATE "testTable" set type_boolean = true 
		WHERE ID IN (SELECT ID FROM "testTable" where type_boolean = false LIMIT 1);`

	_, err = conn.Exec(updateSQL)
	if err != nil {
		t.Fatalf("%s: Could not update row in source database: %v", t.Name(), err)
	}
}

func deleteTestData(t *testing.T, config pgx.ConnConfig, wg *sync.WaitGroup) {
	defer wg.Done()

	conn, err := pgx.Connect(config)
	if err != nil {
		t.Fatalf("%s: Could not connect to source db to delete data", t.Name())
		return
	}
	defer conn.Close()

	// delete a row
	deleteSQL := `DELETE FROM "testTable" WHERE ID IN (SELECT ID FROM "testTable" LIMIT 1);`

	_, err = conn.Exec(deleteSQL)
	if err != nil {
		t.Fatalf("%s: Could not delete row in source database: %v", t.Name(), err)
	}
}

func TestVersionMigration(t *testing.T) {
	flag.Parse() // Parse() must be called explicitly, see TestMain() docs.

	if !*runIntegrationTests {
		t.Log("Integration tests disabled. Use -integration flag to enable.")
		return
	}

	testCases := []struct {
		name   string
		source string
		target string
	}{
		{
			name:   "9.5To9.6",
			source: "9.5",
			target: "9.6",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			// bring up source and target database containers
			_, srcPort, err := createDatabaseContainer(t, ctx, tc.source, dbUser, dbPassword, dbName)
			require.NoError(t, err)
			srcDBConfig := pgx.ConnConfig{
				Host:     "127.0.0.1",
				Port:     uint16(srcPort),
				User:     dbUser,
				Password: dbPassword,
				Database: dbName,
			}
			err = setupTestSchema(srcDBConfig)
			require.NoError(t, err)

			_, targetPort, err := createDatabaseContainer(t, ctx, tc.target, dbUser, dbPassword, dbName)
			require.NoError(t, err)
			targetDBConfig := pgx.ConnConfig{
				Host:     "127.0.0.1",
				Port:     uint16(targetPort),
				User:     dbUser,
				Password: dbPassword,
				Database: dbName,
			}

			err = setupTestSchema(targetDBConfig)
			require.NoError(t, err)

			// setup warp-pipe on source database
			schemas := []string{"public"}
			includeTables := make([]string, 0)
			excludeTables := make([]string, 0)

			wpConn, err := pgx.Connect(srcDBConfig)
			require.NoError(t, err)

			err = db.Prepare(wpConn, []string{"public"}, []string{"testTable"}, []string{})
			require.NoError(t, err)

			// write, update, delete to produce change sets
			var insertsWG, updatesWG, deletesWG sync.WaitGroup
			workersCount := 20
			for i := 0; i < workersCount; i++ {
				insertsWG.Add(1)
				go insertTestData(t, srcDBConfig, &insertsWG)
			}

			for i := 0; i < workersCount; i++ {
				updatesWG.Add(1)
				go updateTestData(t, srcDBConfig, &updatesWG)
			}

			for i := 0; i < workersCount; i++ {
				deletesWG.Add(1)
				go deleteTestData(t, srcDBConfig, &deletesWG)
			}

			// sync source and target with Axon
			axonCfg := warppipe.AxonConfig{
				SourceDBHost:               srcDBConfig.Host,
				SourceDBPort:               srcPort,
				SourceDBName:               srcDBConfig.Database,
				SourceDBUser:               srcDBConfig.User,
				SourceDBPass:               srcDBConfig.Password,
				TargetDBHost:               targetDBConfig.Host,
				TargetDBPort:               targetPort,
				TargetDBName:               targetDBConfig.Database,
				TargetDBUser:               targetDBConfig.User,
				TargetDBPass:               targetDBConfig.Password,
				TargetDBSchema:             "public",
				ShutdownAfterLastChangeset: true,
			}

			axon := warppipe.Axon{Config: &axonCfg}

			// first pass sync.
			axon.Run()

			// wait for all our routines to complete
			insertsWG.Wait()
			updatesWG.Wait()
			deletesWG.Wait()

			// sync one more time to catch any stragglers
			axon.Run()

			err = axon.Verify(schemas, includeTables, excludeTables)
			require.NoError(t, err)
		})
	}
}

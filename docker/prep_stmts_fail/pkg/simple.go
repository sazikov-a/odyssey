package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/petermattis/goid"

	pq "github.com/lib/pq"
)

var dbName = "db1"
var tblName = "sh1.locks"

var insertQuery = fmt.Sprintf(
	`INSERT INTO %s AS t (key, owner, expiration_time)
	VALUES ($1, $2, current_timestamp + make_interval(secs => $3))
    ON CONFLICT (key) DO UPDATE
    SET
		owner = $2,
		expiration_time = current_timestamp + make_interval(secs => $3)
    WHERE (t.owner = $2) OR (t.expiration_time <= current_timestamp)
	RETURNING 1;`, tblName)

func workerRoutine(conn *sqlx.DB, wg *sync.WaitGroup, ctx context.Context, lock string, self string, startAfter time.Duration) {

	var insertPrep *sqlx.Stmt = nil

	var hasSuccessfullCallsAfterDroppingStatements bool = true

	time.Sleep(startAfter)

	go func() {
		defer wg.Done()

		var i int = 0
		for {
			select {
			case <-time.After(time.Duration(1000+rand.Intn(500)) * time.Millisecond):
				log.Printf("task %d (%s): running iteration %d", goid.Get(), self, i)

				if insertPrep == nil {
					var err error = nil
					insertPrep, err = conn.Preparex(insertQuery)
					if err != nil {
						log.Println("failed to prepare statement")
						log.Fatal(err)
					}
				}

				_, err := insertPrep.Exec(lock, self, 1*time.Second)
				if err != nil {
					switch e := err.(type) {
					case *pq.Error:
						switch e.Code.Name() {
						case "invalid_sql_statement_name":
							insertPrep = nil
							hasSuccessfullCallsAfterDroppingStatements = false
							log.Println("received invalid_sql_statement_name: ", e)
						default:
							log.Printf(e.Code.Name(), " ", e.Code.Class())
						}
					default:
					}
				} else {
					hasSuccessfullCallsAfterDroppingStatements = true
				}

				i += 1

			case <-ctx.Done():
				if hasSuccessfullCallsAfterDroppingStatements {
					log.Fatalf("%s received `invalid_sql_statement_name` from PG, but later executions of prepared statements were not successfull\n", self)
				}

				return
			}

		}

	}()
}

func initDb(conn *sqlx.DB) {
	_, err := conn.Exec(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (key TEXT PRIMARY KEY, owner TEXT, expiration_time TIMESTAMPTZ);", tblName))
	if err != nil {
		log.Fatal(err)
	}
}

func cleanDb(conn *sqlx.DB) {
	_, err := conn.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s;", tblName))
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	ctx := context.TODO()

	db, err := sqlx.ConnectContext(ctx, "postgres", fmt.Sprintf("host=localhost port=6432 user=user1 dbname=%s sslmode=disable", dbName))
	if err != nil {
		log.Fatal(err)
	}

	initDb(db)
	defer cleanDb(db)

	wg := sync.WaitGroup{}
	routineCtx, cancel := context.WithCancel(context.Background())
	wg.Add(10)
	for i := 0; i < 10; i++ {
		db_i, err := sqlx.ConnectContext(ctx, "postgres", fmt.Sprintf("host=localhost port=6432 user=user1 dbname=%s sslmode=disable", dbName))
		if err != nil {
			log.Fatal(err)
		}

		startAfter := 0 * time.Second

		if i > 0 {
			startAfter = 80 * time.Second
		}

		go workerRoutine(db_i, &wg, routineCtx, fmt.Sprintf("lock-%d", i%4), fmt.Sprintf("worker-%d", i), startAfter)

		time.Sleep(1 * time.Second)
	}

	time.Sleep(30 * time.Second)

	log.Println("Dropping column")

	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s DROP COLUMN owner;", tblName))
	if err != nil {
		log.Fatal(err)
	}

	time.Sleep(30 * time.Second)

	log.Println("Blocking connection one physical")
	_, err = db.Exec("SELECT pg_sleep(70)")
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Unblocking connection one physical")

	time.Sleep(30 * time.Second)

	log.Println("Adding column back")

	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN owner TEXT;", tblName))
	if err != nil {
		log.Fatal(err)
	}

	time.Sleep(30 * time.Second)

	log.Println("Finishing")

	cancel()

	wg.Wait()

	log.Println("TEST OK")
}


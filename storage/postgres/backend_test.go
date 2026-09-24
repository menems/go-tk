package postgres_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/menems/go-tk/storage/postgres"
)

// The queries the test backend answers, each with the reply a PostgreSQL
// server would send.
const (
	queryNoRow     = "select no row"
	queryCollide   = "insert collide"
	queryForeign   = "insert foreign"
	querySerialize = "update serialize"
	queryTimedOut  = "select timed out"
	queryClose     = "select close"
)

// collisionIndex and collisionDetail are what the backend sends for
// queryCollide; the detail quotes the colliding row, as PostgreSQL's does.
const (
	collisionIndex  = "users_email_key"
	collisionDetail = "Key (email)=(ada@example.com) already exists."
)

// refusals maps a query to the SQLSTATE the backend refuses it with.
var refusals = map[string]string{
	queryCollide:   "23505",
	queryForeign:   "23503",
	querySerialize: "40001",
	queryTimedOut:  "57014",
}

// newPool serves a PostgreSQL backend on a loopback listener the test owns and
// returns a pool from New pointed at it. The simple protocol keeps the backend
// to one message per statement; the failures it returns are the ones the
// extended protocol returns.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool, _ := newRecordingPool(t, "")
	return pool
}

// startups holds the parameters of every startup message the backend received.
type startups struct {
	mu     sync.Mutex
	params []map[string]string
}

func (s *startups) record(params map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.params = append(s.params, params)
}

// all returns the parameters of each startup message received so far.
func (s *startups) all() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]string(nil), s.params...)
}

// newRecordingPool is newPool with dsnParams appended to the DSN's query and
// opts handed to New, returning what the backend records of each connection's
// startup message.
func newRecordingPool(t *testing.T, dsnParams string, opts ...postgres.Option) (*pgxpool.Pool, *startups) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	seen := &startups{}
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() { serve(conn, seen) })
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})

	dsn := "postgres://user:pass@" + ln.Addr().String() + "/testdb?sslmode=disable&default_query_exec_mode=simple_protocol" + dsnParams
	pool, err := postgres.New(context.Background(), dsn, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, seen
}

func serve(conn net.Conn, seen *startups) {
	defer conn.Close()
	be := pgproto3.NewBackend(conn, conn)

	for {
		msg, err := be.ReceiveStartupMessage()
		if err != nil {
			return
		}
		if startup, ok := msg.(*pgproto3.StartupMessage); ok {
			seen.record(startup.Parameters)
			break
		}
		// SSL or GSS encryption request: refuse it, the client goes on in clear.
		if _, err := conn.Write([]byte{'N'}); err != nil {
			return
		}
	}
	be.Send(&pgproto3.AuthenticationOk{})
	be.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	be.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if be.Flush() != nil {
		return
	}

	txStatus := byte('I')
	for {
		msg, err := be.Receive()
		if err != nil {
			return
		}
		q, ok := msg.(*pgproto3.Query)
		if !ok {
			return
		}

		switch code, refused := refusals[q.String]; {
		case q.String == queryClose:
			return
		case q.String == "begin":
			txStatus = 'T'
			be.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
		case q.String == "rollback":
			txStatus = 'I'
			be.Send(&pgproto3.CommandComplete{CommandTag: []byte("ROLLBACK")})
		case q.String == queryNoRow:
			be.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{
				{Name: []byte("n"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1},
			}})
			be.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 0")})
		case refused:
			if txStatus == 'T' {
				txStatus = 'E'
			}
			refusal := &pgproto3.ErrorResponse{Severity: "ERROR", Code: code, Message: "refused"}
			if code == "23505" {
				refusal.Message = `duplicate key value violates unique constraint "` + collisionIndex + `"`
				refusal.Detail = collisionDetail
				refusal.ConstraintName = collisionIndex
			}
			be.Send(refusal)
		default:
			be.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42601", Message: "unexpected query " + q.String})
		}
		be.Send(&pgproto3.ReadyForQuery{TxStatus: txStatus})
		if be.Flush() != nil {
			return
		}
	}
}

// failWith runs query on pool and returns the error pgx hands back.
func failWith(t *testing.T, pool *pgxpool.Pool, query string) error {
	t.Helper()

	var err error
	if query == queryNoRow {
		var n int
		err = pool.QueryRow(context.Background(), query).Scan(&n)
	} else {
		_, err = pool.Exec(context.Background(), query)
	}
	if err == nil {
		t.Fatalf("%q: expected an error, got nil", query)
	}
	return err
}

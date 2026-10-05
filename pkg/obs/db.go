package obs

import (
	"database/sql"
	"database/sql/driver"
	"fmt"

	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel/attribute"
)

// OpenDB is a drop-in replacement for sql.Open that traces every query and
// exports connection-pool metrics.
//
// It is the reason a slow request is diagnosable rather than merely visible: a
// span tree showing 400ms inside a single SELECT answers the question that a
// latency histogram only raises. The pool metrics matter here specifically
// because Vault mints a NEW database role every 45 minutes
// (gitops/apps/*/credentials/dynamic), so connection churn is a normal part of
// this system's behaviour and worth being able to see.
//
// The driver itself must still be registered by the caller -- the usual blank
// import of github.com/lib/pq. Keeping that in the service rather than in here
// is what stops this package from quietly becoming Postgres-specific.
func OpenDB(driverName, dsn string) (*sql.DB, error) {
	attrs := otelsql.WithAttributes(attribute.String("db.system", "postgresql"))

	db, err := otelsql.Open(driverName, dsn, attrs)
	if err != nil {
		return nil, err
	}
	// The returned Registration is deliberately discarded: this pool lives for
	// the lifetime of the process, so there is never a point at which we would
	// want to stop reporting its stats. A registration failure is also not a
	// reason to refuse to serve traffic, so it is reported and swallowed.
	if _, err := otelsql.RegisterDBStatsMetrics(db, attrs); err != nil {
		Logger().Warn("could not register database pool metrics", "error", fmt.Sprint(err))
	}
	return db, nil
}

// OpenDBConnector is OpenDB for a caller that supplies its own driver.Connector
// rather than a DSN string.
//
// It exists for pkg/pgcreds, whose connector re-reads the credential from a
// mounted Secret on every connection so that a Vault rotation needs no restart.
// A DSN is a fixed string and cannot express that, which is precisely how the
// credential went stale in the first place.
//
// Instrumentation is identical to OpenDB's: the point of routing both through
// here is that a span and the pool metrics do not depend on which way the
// connection was configured.
func OpenDBConnector(c driver.Connector) *sql.DB {
	attrs := otelsql.WithAttributes(attribute.String("db.system", "postgresql"))

	db := otelsql.OpenDB(c, attrs)
	if _, err := otelsql.RegisterDBStatsMetrics(db, attrs); err != nil {
		Logger().Warn("could not register database pool metrics", "error", fmt.Sprint(err))
	}
	return db
}

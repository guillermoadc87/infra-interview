// Package pgcreds opens a PostgreSQL pool whose credentials are read from disk
// ON EVERY NEW CONNECTION, so a rotated credential is picked up without
// restarting the process.
//
// WHY THIS EXISTS. Vault's database secrets engine mints a brand new PostgreSQL
// role per application with a 1h TTL and revokes the old one on expiry. The
// credential was injected as DB_USER/DB_PASSWORD environment variables, and env
// vars are read once at container start -- so a rotation only took effect if
// something restarted the pod.
//
// Stakater Reloader was that something, and it stopped working when the
// workloads moved behind an Argo Rollout. Reloader restarts a workload by
// patching its pod template; under `workloadRef` the template lives on a
// Deployment that the Rollout has scaled to ZERO, and the Rollout itself has no
// template to patch -- adding one puts it straight into Degraded (measured).
// Reloader's source confirms it: it only ever writes Spec.Template.Annotations
// and never touches spec.restartAt.
//
// The symptom was ugly and silent. Vault rotated, the pod kept the old
// credential, Vault then revoked that role's privileges, and every request
// started failing with
//
//	pq: permission denied for table orders
//
// while the pod stayed Running and Ready -- because readiness pings the database
// and a ping succeeds for a role that can connect but cannot SELECT.
//
// THE FIX IS TO NOT NEED A RESTART. Kubernetes updates a mounted Secret's files
// in place (kubelet sync, ~1 minute), so the credential on disk is always
// current. This package reads it at connection time rather than at start-up,
// which is what the repo's own comment in
// gitops/apps/*/credentials/dynamic/resources.yaml already called the right
// answer: "a Vault Agent sidecar (which can re-read a credential without a
// restart) is the usual production choice".
package pgcreds

import (
	"context"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Config describes where the credential lives and what to connect to.
type Config struct {
	// Dir holds the mounted Secret: one file named `username`, one `password`.
	Dir string
	// Host, Port and Database are ordinary configuration, not secrets.
	Host     string
	Port     string
	Database string
	// SSLMode is passed through; the in-cluster Postgres has TLS disabled.
	SSLMode string
}

// Connector reads the credential from disk for EVERY connection it opens.
//
// database/sql calls Connect whenever the pool needs a new connection, so a
// rotated credential is adopted the next time the pool grows or recycles --
// which is why MaxConnLifetime below is the other half of this design.
type Connector struct{ cfg Config }

// New validates the configuration and returns a connector.
//
// It deliberately does NOT read the credential here: doing so would cache it for
// the process lifetime and reintroduce exactly the bug this package exists to
// remove. The files are only checked for existence, so a misconfigured mount
// fails at start-up rather than at the first query.
func New(cfg Config) (*Connector, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("pgcreds: Dir is required")
	}
	for _, f := range []string{"username", "password"} {
		if _, err := os.Stat(filepath.Join(cfg.Dir, f)); err != nil {
			return nil, fmt.Errorf("pgcreds: %s not readable in %s: %w", f, cfg.Dir, err)
		}
	}
	return &Connector{cfg: cfg}, nil
}

// Connect reads the current credential and dials.
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	user, err := c.read("username")
	if err != nil {
		return nil, err
	}
	pass, err := c.read("password")
	if err != nil {
		return nil, err
	}

	inner, err := pq.NewConnector(c.dsn(user, pass))
	if err != nil {
		return nil, fmt.Errorf("pgcreds: building connector: %w", err)
	}
	return inner.Connect(ctx)
}

// Driver satisfies driver.Connector.
func (c *Connector) Driver() driver.Driver { return pq.Driver{} }

// Username reports the credential currently on disk. For diagnostics only --
// nothing should hold on to the result.
func (c *Connector) Username() string {
	u, err := c.read("username")
	if err != nil {
		return "<unreadable>"
	}
	return u
}

func (c *Connector) read(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(c.cfg.Dir, name))
	if err != nil {
		return "", fmt.Errorf("pgcreds: reading %s: %w", name, err)
	}
	// Kubernetes writes Secret values verbatim; trim so a trailing newline
	// introduced by any other producer cannot corrupt the password.
	return strings.TrimRight(string(b), "\r\n"), nil
}

func (c *Connector) dsn(user, pass string) string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.cfg.Host, c.cfg.Port, user, pass, c.cfg.Database, c.cfg.SSLMode)
}

// MaxConnLifetime is the other half of the design, and the reason this is a
// named constant rather than a number buried in main.
//
// Reading the credential per connection is not enough on its own: a connection
// already established with the OLD role stays open and keeps working until
// Vault revokes that role, at which point the SAME pooled connection starts
// failing with "permission denied" mid-request. Capping the lifetime forces the
// pool to recycle, so connections are re-established with whatever is on disk.
//
// 5 minutes against a 1h credential TTL and a 45m refresh leaves a wide margin:
// a new credential is adopted within ~5 minutes of appearing, and the old role
// is not revoked for another ~15.
const MaxConnLifetime = 5 * time.Minute

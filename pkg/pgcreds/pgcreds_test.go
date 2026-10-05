package pgcreds

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCreds(t *testing.T, dir, user, pass string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "username"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "password"), []byte(pass), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNewFailsLoudlyOnAMisconfiguredMount(t *testing.T) {
	// Better at start-up than at the first query: a missing mount is a
	// deployment mistake, and discovering it when a user hits an endpoint is
	// the worst time to find out.
	if _, err := New(Config{Dir: t.TempDir()}); err == nil {
		t.Fatal("expected an error when username/password are absent")
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected an error when Dir is empty")
	}
}

func TestCredentialIsReadPerCallNotCached(t *testing.T) {
	// THE WHOLE POINT. Caching the credential at construction would reintroduce
	// exactly the bug this package exists to remove -- a process holding a
	// credential Vault has since revoked.
	dir := t.TempDir()
	writeCreds(t, dir, "v-role-one", "pw-one")

	c, err := New(Config{Dir: dir, Host: "h", Port: "5432", Database: "orders", SSLMode: "disable"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Username(); got != "v-role-one" {
		t.Fatalf("username = %q, want v-role-one", got)
	}

	// Simulate what the kubelet does when External Secrets refreshes the Secret.
	writeCreds(t, dir, "v-role-two", "pw-two")

	if got := c.Username(); got != "v-role-two" {
		t.Fatalf("after rotation username = %q, want v-role-two -- the credential was cached", got)
	}
}

func TestTrailingNewlineIsStripped(t *testing.T) {
	// Kubernetes writes Secret values verbatim, but anything else that produces
	// these files (a human, a script, an editor) is likely to add a newline --
	// and a password with a trailing \n fails authentication with a message
	// that says nothing about whitespace.
	dir := t.TempDir()
	writeCreds(t, dir, "v-role\n", "pw\r\n")

	c, err := New(Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Username(); got != "v-role" {
		t.Fatalf("username = %q, want %q", got, "v-role")
	}
	if dsn := c.dsn("u", "p"); dsn == "" {
		t.Fatal("dsn should not be empty")
	}
}

func TestDSNCarriesEveryField(t *testing.T) {
	c := &Connector{cfg: Config{Host: "pg.svc", Port: "5432", Database: "orders", SSLMode: "disable"}}
	got := c.dsn("alice", "s3cret")
	for _, want := range []string{"host=pg.svc", "port=5432", "user=alice", "password=s3cret", "dbname=orders", "sslmode=disable"} {
		if !contains(got, want) {
			t.Errorf("dsn %q missing %q", got, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

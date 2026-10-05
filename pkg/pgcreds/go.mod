// Second shared module alongside pkg/obs, same no-domain convention so
// scripts/set-owner.sh has nothing to stamp. Resolved only through a `replace`.
module infra-interview/pkg/pgcreds

go 1.26.0

require github.com/lib/pq v1.10.9

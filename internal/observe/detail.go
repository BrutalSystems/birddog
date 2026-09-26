package observe

import "github.com/BrutalSystems/birddog/internal/policy"

// maxDetail bounds what an observed request's subject may contribute to the
// durable record. It matches the bound the opencode plugin applies when it
// writes one.
//
// The bound is duplicated deliberately. The plugin and this binary ship from
// the same tag in the same version, but opencode resolves an npm specifier
// once into a package cache and never revisits it, so a session can keep
// loading an older plugin long after a newer one was published — a drift the
// provider notes record as real and invisible. A reader that trusts a bound it
// does not itself apply is trusting a writer whose version it cannot see.
//
// It is also the one path by which unbounded text from a watched session
// reaches the log at all. A permission request's subject is a command line, a
// path, or an edit target, and a command line can carry a credential. birddog
// records evidence, not payloads.
const maxDetail = policy.MaxDetail

// boundDetail limits a request subject to maxDetail runes, marking any value
// it had to cut. See policy.BoundDetail, which is where the rule lives now
// that more than one package must apply it.
func boundDetail(s string) string { return policy.BoundDetail(s) }

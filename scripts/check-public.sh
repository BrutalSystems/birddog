#!/bin/sh
# check-public.sh — refuse to publish text that does not belong in a public
# repository.
#
# This repository is public: everything pushed to it, commit messages
# included, can be read by anyone. This script greps what is about to leave
# the machine — the lines a push adds, its commit messages and its ref names —
# for two kinds of pattern:
#
#   * generic ones shipped here: home-directory paths, email addresses, and
#     the shapes of common API tokens and private keys;
#   * a private list of terms kept outside every repository, one
#     case-insensitive extended regex per line ('#' starts a comment). It is
#     read from $PUBLIC_TERMS_FILE, else `git config public.termsFile`, else
#     ~/.config/public-terms.txt. A missing list is reported, not an error.
#
# Any hit exits 1 and prints where it was found.
#
# Usage:
#   scripts/check-public.sh                 check origin/main..HEAD
#   scripts/check-public.sh <range>         check a revision range
#   scripts/check-public.sh --tree          check every tracked file as it is now
#   scripts/check-public.sh --pre-push      read refs from stdin, as git's
#                                           pre-push hook does
#
# `make hooks` installs it as the pre-push hook. System grep is used rather
# than `git grep`: macOS git grep ignores \b, which the term list relies on.

set -eu

# The system grep, never a shell function or alias wrapping another grep.
GREP=/usr/bin/grep
[ -x "$GREP" ] || GREP=$(command -v grep)

# Generic patterns: case-sensitive ERE. Placeholder home directories and
# example.com addresses are excluded so tests and docs can still use them.
# Token prefixes are written with a bracket so this file passes its own check.
GENERIC='(/Users|/home)/[A-Za-z0-9._-]+/'
GENERIC_ALLOW='(/Users|/home)/(x|u|me|you|user|someone|a-user[A-Za-z0-9-]*|runner|USER|<[^>]*>)/'
EMAIL='[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}'
EMAIL_ALLOW='noreply|@example\.(com|org|net)|@users\.noreply\.github\.com'
TOKENS='AKI[A][0-9A-Z]{16}|gh[p]_[A-Za-z0-9]{20,}|gh[o]_[A-Za-z0-9]{20,}|github[_]pat_[A-Za-z0-9_]{20,}|np[m]_[A-Za-z0-9]{30,}|sk[-]ant-[A-Za-z0-9_-]{10,}|xo[x][abp]-[A-Za-z0-9-]{10,}|BEGI[N] [A-Z ]*PRIVATE KEY'

terms_file=${PUBLIC_TERMS_FILE:-$(git config --get public.termsFile 2>/dev/null || true)}
terms_file=${terms_file:-$HOME/.config/public-terms.txt}
case $terms_file in "~/"*) terms_file=$HOME/${terms_file#"~/"} ;; esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
: >"$work/text"

if [ -r "$terms_file" ]; then
	"$GREP" -vE '^[[:space:]]*(#|$)' "$terms_file" >"$work/terms" || true
else
	echo "check-public: no private term list at $terms_file — checking generic patterns only" >&2
	: >"$work/terms"
fi

# collect <label> appends labelled lines from stdin to the text under check.
collect() {
	sed "s|^|$1: |" >>"$work/text"
}

# range <rev-range> adds what that range would publish.
range() {
	git log --format='%H%n%B' "$@" | collect "message"
	git log -p --format='commit %H' --no-color "$@" |
		awk '/^commit [0-9a-f]+$/ { c = substr($2, 1, 12); next }
		     /^\+\+\+ / { f = substr($0, 7); next }
		     /^\+/ { print c " " f ": " substr($0, 2) }' |
		collect "added"
}

zero=0000000000000000000000000000000000000000

case ${1:-} in
--pre-push)
	while read -r local_ref local_sha remote_ref remote_sha; do
		[ "$local_sha" = "$zero" ] && continue # a delete publishes nothing
		echo "$remote_ref" | collect "ref"
		if [ "$remote_sha" = "$zero" ] || ! git cat-file -e "$remote_sha" 2>/dev/null; then
			range "$local_sha" --not --remotes
		else
			range "$remote_sha..$local_sha"
		fi
		case $local_ref in refs/tags/*)
			git cat-file -p "$local_sha" 2>/dev/null | collect "tag" ;;
		esac
	done
	;;
--tree)
	git ls-files -z | xargs -0 "$GREP" -nIH '' 2>/dev/null | collect "tree" || true
	;;
"")
	range origin/main..HEAD
	;;
*)
	range "$1"
	;;
esac

hits=$work/hits
: >"$hits"

# match <grep args...> appends matching lines to $hits. grep exits 1 for no
# match and 2 for an error — a pattern it cannot compile must stop the push,
# not pass it as clean.
match() {
	rc=0
	"$GREP" "$@" "$work/text" >"$work/m" || rc=$?
	if [ "$rc" -gt 1 ]; then
		echo "check-public: grep failed on a pattern (exit $rc) — fix the pattern; refusing" >&2
		exit 2
	fi
	cat "$work/m" >>"$work/raw"
}

: >"$work/raw"
match -E "$GENERIC"
"$GREP" -vE "$GENERIC_ALLOW" "$work/raw" >>"$hits" || true
: >"$work/raw"
match -E "$EMAIL"
"$GREP" -vE "$EMAIL_ALLOW" "$work/raw" >>"$hits" || true
: >"$work/raw"
match -E "$TOKENS"
if [ -s "$work/terms" ]; then
	match -iE -f "$work/terms"
fi
cat "$work/raw" >>"$hits"

if [ -s "$hits" ]; then
	echo "check-public: refusing — these lines would be published:" >&2
	sort -u "$hits" | cut -c1-240 >&2
	exit 1
fi
echo "check-public: clean" >&2

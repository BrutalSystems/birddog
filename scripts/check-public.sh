#!/bin/sh
# check-public.sh — refuse to publish text that does not belong in a public
# repository.
#
# This repository is public: everything pushed to it, commit messages
# included, can be read by anyone. This script greps what is about to leave
# the machine — the lines a push adds, the paths it touches, its commit
# messages, tag messages and ref names — for two kinds of pattern:
#
#   * generic ones shipped here: home-directory paths, email addresses, and
#     the shapes of common API tokens and private keys;
#   * a private list of terms kept outside every repository, one
#     case-insensitive extended regex per line ('#' starts a comment). It is
#     read from $PUBLIC_TERMS_FILE, else `git config public.termsFile`, else
#     ~/.config/public-terms.txt. A missing or unreadable list refuses the
#     check; set PUBLIC_TERMS_FILE=none to check the generic patterns only.
#
# Any hit exits 1 and prints where it was found. Anything the script cannot
# read or verify — a git error, a shallow clone, a pattern grep rejects —
# exits 2. It never reports clean on a check it could not finish.
#
# Usage:
#   scripts/check-public.sh                 check origin/main..HEAD
#   scripts/check-public.sh <range>         check a revision range
#   scripts/check-public.sh --tree          check every tracked file as it is now
#   scripts/check-public.sh --pre-push <remote> [<url>]
#                                           read refs from stdin, as git's
#                                           pre-push hook does
#
# `make hooks` installs it as the pre-push hook. System grep is used rather
# than `git grep`: macOS git grep ignores \b, which the term list relies on.
# Binary files are not inspected.

set -eu

# The system grep, never a shell function or alias wrapping another grep.
GREP=/usr/bin/grep
[ -x "$GREP" ] || GREP=$(command -v grep)

# Generic patterns: case-sensitive ERE. Placeholder home directories and
# example.com addresses are blanked out before matching, match by match, so
# tests and docs can still use them beside nothing else.
# Token prefixes are written with a bracket so this file passes its own check.
GENERIC='(/Users|/home)/[A-Za-z0-9._-]+/'
GENERIC_ALLOW='(/Users|/home)/(x|u|me|you|user|someone|a-user[A-Za-z0-9-]*|runner|USER|<[^>]*>)/'
EMAIL='[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}'
# Each allowance must match a whole address: the character after it may not
# continue the domain, so an address at example.company is not excused.
EMAIL_ALLOW='[A-Za-z0-9._%+-]*noreply@[A-Za-z0-9.-]+|[A-Za-z0-9._%+-]+@example\.(com|org|net)([^A-Za-z0-9.-]|$)'
TOKENS='AKI[A][0-9A-Z]{16}|gh[p]_[A-Za-z0-9]{20,}|gh[o]_[A-Za-z0-9]{20,}|github[_]pat_[A-Za-z0-9_]{20,}|np[m]_[A-Za-z0-9]{30,}|sk[-]ant-[A-Za-z0-9_-]{10,}|xo[x][abp]-[A-Za-z0-9-]{10,}|BEGI[N] [A-Z ]*PRIVATE KEY'

fail() {
	echo "check-public: $* — refusing" >&2
	exit 2
}

work=$(mktemp -d) || fail "cannot make a temporary directory"
trap 'rm -rf "$work"' EXIT
text=$work/text
: >"$text"

# --- the private term list -------------------------------------------------

terms_file=${PUBLIC_TERMS_FILE:-}
if [ -z "$terms_file" ]; then
	# git config exits 1 when the key is unset; anything else is an error.
	rc=0
	terms_file=$(git config --get public.termsFile) || rc=$?
	[ "$rc" -le 1 ] || fail "cannot read git config public.termsFile"
fi
terms_file=${terms_file:-$HOME/.config/public-terms.txt}
case $terms_file in "~/"*) terms_file=$HOME/${terms_file#"~/"} ;; esac
: >"$work/terms"
if [ "$terms_file" != none ]; then
	[ -r "$terms_file" ] || fail "no readable term list at $terms_file (PUBLIC_TERMS_FILE=none checks generic patterns only)"
	rc=0
	"$GREP" -vE '^[[:space:]]*(#|$)' "$terms_file" >"$work/terms" || rc=$?
	[ "$rc" -le 1 ] || fail "cannot read $terms_file"
fi

# --- collecting what would be published ------------------------------------

# git_to <file> <git args...> runs git with its output in a file, so a git
# failure is seen rather than hidden at the head of a pipeline.
git_to() {
	out=$1
	shift
	git "$@" >"$out" || fail "git $* failed"
}

# label <label> <file> appends the file's lines to the text under check.
label() {
	sed "s|^|$1: |" "$2" >>"$text"
}

# commits <rev-list args...> adds what those commits publish: each message,
# and for each commit the paths and added lines of its diff against its first
# parent (all of a root commit; a merge shows what it brings to the branch).
commits() {
	git_to "$work/revs" rev-list "$@"
	while IFS= read -r c; do
		git_to "$work/msg" log -1 --format=%B "$c"
		label "message ${c%"${c#????????????}"}" "$work/msg"
		git_to "$work/diff" -c core.quotepath=off show --no-ext-diff --no-textconv \
			--diff-merges=first-parent --format= --no-color --src-prefix=a/ --dst-prefix=b/ "$c"
		awk -v c="${c%"${c#????????????}"}" '
			/^diff --git / { hdr = 1; f = $0; print c " path: " substr($0, 12); next }
			hdr && /^(rename|copy) (from|to) / { print c " path: " $0; next }
			hdr && /^\+\+\+ / { f = substr($0, 7); next }
			/^@@/ { hdr = 0; next }
			!hdr && /^\+/ { print c " " f ": " substr($0, 2) }
		' "$work/diff" >"$work/added" || fail "cannot parse the diff of $c"
		label "added" "$work/added"
	done <"$work/revs"
}

# tag <object> adds an annotated tag's message, following tags of tags, and
# the commits the tag finally points at.
tag() {
	obj=$1
	while [ "$(git cat-file -t "$obj")" = tag ]; do
		git_to "$work/tag" cat-file tag "$obj"
		label "tag" "$work/tag"
		obj=$(sed -n '1s/^object //p' "$work/tag")
	done
	echo "$obj"
}

is_zero() {
	case $1 in *[!0]*) return 1 ;; *) return 0 ;; esac
}

[ "$(git rev-parse --is-shallow-repository)" = false ] ||
	fail "this clone is shallow, so the history a push publishes cannot all be read"

case ${1:-} in
--pre-push)
	remote=${2:-}
	[ -n "$remote" ] || fail "--pre-push needs the remote name git passes the hook"
	while read -r local_ref local_sha remote_ref remote_sha; do
		is_zero "$local_sha" && continue # a delete publishes nothing
		printf '%s\n%s\n' "$remote_ref" "$local_ref" >"$work/ref"
		label "ref" "$work/ref"
		type=$(git cat-file -t "$local_sha") || fail "cannot read $local_sha"
		target=$local_sha
		if [ "$type" = tag ]; then
			target=$(tag "$local_sha")
			type=$(git cat-file -t "$target") || fail "cannot read $target"
		fi
		[ "$type" = commit ] || fail "$remote_ref points at a $type, which this check cannot inspect"
		# What the remote already has needs no checking: its old value for
		# this ref, and what its remote-tracking refs say it holds. Only refs
		# of this remote count — another remote's history is not on it.
		if ! is_zero "$remote_sha" && git cat-file -e "$remote_sha^{commit}" 2>/dev/null; then
			commits "$target" "^$remote_sha" --not --remotes="$remote"
		else
			commits "$target" --not --remotes="$remote"
		fi
	done
	;;
--tree)
	git_to "$work/files" ls-files -z
	if [ -s "$work/files" ]; then
		# grep exits 1 for a file with no text lines (empty or binary), which
		# is fine, and 2 for one it cannot read, which is not. xargs reports
		# those differently on BSD and GNU, so each grep's status is settled
		# inside the wrapper and only a real failure leaves it non-zero.
		rc=0
		xargs -0 sh -c '"$0" -nIH "" "$@"; s=$?; [ "$s" -le 1 ] || exit 255' "$GREP" \
			<"$work/files" >"$work/tree" || rc=$?
		[ "$rc" -eq 0 ] || fail "cannot read the tracked files"
		label "tree" "$work/tree"
	fi
	;;
"")
	commits origin/main..HEAD
	;;
*)
	commits "$1"
	;;
esac

# --- matching ----------------------------------------------------------------

hits=$work/hits
: >"$hits"

# match <file> <grep args...> appends the file's matching lines to $hits.
# grep exits 1 for no match and 2 for an error — a pattern it cannot compile
# must stop the push, not pass it as clean.
match() {
	in=$1
	shift
	rc=0
	"$GREP" "$@" "$in" >>"$hits" || rc=$?
	[ "$rc" -le 1 ] || fail "grep failed on a pattern (exit $rc)"
}

# Allowed placeholders are blanked one match at a time, so a placeholder on a
# line does not excuse a real path or address beside it.
sed -E "s#$GENERIC_ALLOW#<placeholder>/#g; s#$EMAIL_ALLOW#<placeholder>\2#g" "$text" >"$work/generic" ||
	fail "cannot blank placeholders"
match "$work/generic" -E "$GENERIC"
match "$work/generic" -E "$EMAIL"
match "$text" -E "$TOKENS"
if [ -s "$work/terms" ]; then
	match "$text" -iE -f "$work/terms"
fi

if [ -s "$hits" ]; then
	echo "check-public: refusing — these lines would be published:" >&2
	sort -u "$hits" | cut -c1-240 >&2
	exit 1
fi
echo "check-public: clean" >&2

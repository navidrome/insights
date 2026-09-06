#!/bin/sh
# Compress the plain-JSON summaries older builds wrote into the .json.gz form the current one
# reads. A one-time pass: nothing writes the plain form any more, and the reader accepts both, so
# this only reclaims disk (about 27 MB down to 9 MB as of Aug 2026).
#
#   sh compress-summaries.sh [-n] [data-folder]
#
#     -n   list what would change and touch nothing
#
# data-folder defaults to the current directory and must be the one holding summaries/, which on
# the production host is the deploy directory mounted at /app.
#
# Safe to interrupt and safe to run twice. Each day is converted through a temporary file that is
# read back and compared before the plain file is unlinked, so a day is never left unreadable: at
# worst both forms are on disk for a moment, and the reader already prefers the .gz.
#
# Only summaries/YYYY/MM/summary-YYYY-MM-DD.json is touched. Copies nested deeper, like the
# hand-made summaries/2026/04/bkp/, are left alone -- the charts ignore those too.
set -eu

dry_run=0
if [ "${1-}" = "-n" ]; then
	dry_run=1
	shift
fi

data_folder=${1-.}
summaries="$data_folder/summaries"

if [ ! -d "$summaries" ]; then
	echo "No summaries directory at $summaries" >&2
	exit 1
fi

# The depth bounds are what pin the YYYY/MM layout: a wildcard in -path would also match a
# slash, so anything nested deeper is only out of scope because of these. -name never matches
# the .json.gz form, whose name ends in .gz.
find "$summaries" -mindepth 3 -maxdepth 3 -type f -name "summary-*.json" | sort | while read -r plain; do
	gz="$plain.gz"

	# An interrupted earlier run: the .gz is already written and is the copy the reader uses, so
	# the plain file is stale and goes without being re-encoded over it.
	if [ -f "$gz" ]; then
		if [ "$dry_run" = 1 ]; then
			echo "would remove superseded $plain"
		else
			rm -f "$plain"
			echo "removed superseded $plain"
		fi
		continue
	fi

	if [ "$dry_run" = 1 ]; then
		echo "would compress $plain"
		continue
	fi

	tmp="$plain.gz.tmp"
	rm -f "$tmp"

	# Read the result back and compare it byte for byte with the original before anything is
	# unlinked. A full disk is the failure this guards against: gzip can write a short file and
	# still exit 0 on some systems, and the plain file is the only copy of that day.
	if ! gzip -c -- "$plain" >"$tmp" 2>/dev/null || ! gzip -cd -- "$tmp" 2>/dev/null | cmp -s - "$plain"; then
		echo "FAILED to compress $plain, leaving it as it is" >&2
		rm -f "$tmp"
		continue
	fi

	# mv within one directory is atomic, so a reader sees either no .gz or a complete one.
	mv -- "$tmp" "$gz"
	rm -f "$plain"
	echo "compressed $plain"
done

# The loop above runs in a subshell on POSIX sh, so nothing it counted survives it. Count what is
# on disk instead, which is the honest number either way.
remaining=$(find "$summaries" -mindepth 3 -maxdepth 3 -type f -name "summary-*.json" | wc -l | tr -d ' ')
compressed=$(find "$summaries" -mindepth 3 -maxdepth 3 -type f -name "summary-*.json.gz" | wc -l | tr -d ' ')

if [ "$dry_run" = 1 ]; then
	echo "dry run: $remaining plain, $compressed compressed"
	exit 0
fi

echo "done: $compressed compressed, $remaining plain left"
if [ "$remaining" != 0 ]; then
	echo "the $remaining plain file(s) above could not be converted; they are still readable" >&2
	exit 1
fi

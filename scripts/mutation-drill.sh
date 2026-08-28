#!/usr/bin/env bash
# Prove that the intent fences fail when the rule they name is removed.
#
# Why this exists: a test that passes with its own subject deleted is not a
# fence, it is decoration. One of the filters shipped on 2026-08-28 had exactly
# that problem — TestBochaDropsPagesThatAreNotAboutHiring passed with
# isRecruitment deleted, because its decoys were removed by a different filter
# first. The only way to know is to break the rule on purpose and watch the
# named test go red.
#
# Each drill edits a source file in place, runs one test, and restores the file
# whatever happens. `-count=1` is not optional: a cached PASS from an earlier
# run would report a live fence as dead, and a cached FAIL would do the reverse.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOWORK=off

pass=0
fail=0

# A drill leaves a mutated source file on disk for the length of one `go test`.
# Interrupting it there would leave the mutation behind, so the restore is armed
# on the signals as well as on the normal path — a half-applied drill that looks
# like an edit somebody made on purpose is the worst outcome this can have.
CURRENT_FILE=""
CURRENT_BACKUP=""
restore_current() {
  if [ -n "$CURRENT_FILE" ] && [ -f "$CURRENT_BACKUP" ]; then
    cp "$CURRENT_BACKUP" "$CURRENT_FILE"
    rm -f "$CURRENT_BACKUP"
    CURRENT_FILE=""
    CURRENT_BACKUP=""
  fi
}
trap 'restore_current' EXIT INT TERM

# drill <name> <file> <python-mutation> <test-regex> <package>
drill() {
  local name="$1" file="$2" mutation="$3" test="$4" pkg="$5"
  CURRENT_BACKUP="$(mktemp)"
  cp "$file" "$CURRENT_BACKUP"
  CURRENT_FILE="$file"
  # shellcheck disable=SC2064
  trap 'restore_current' RETURN

  if ! python3 -c "$mutation" "$file"; then
    echo "  SKIP $name — the mutation no longer applies; the code moved under it" >&2
    fail=$((fail + 1))
    return 0
  fi
  if go test "$pkg" -run "$test" -count=1 >/dev/null 2>&1; then
    echo "  NOT A FENCE  $name"
    echo "               $test passed with the rule removed"
    fail=$((fail + 1))
  else
    echo "  fence holds  $name"
    pass=$((pass + 1))
  fi
}

# Replace the first occurrence of OLD with NEW, failing loudly if OLD is gone.
py_sub() {
  cat <<PY
import sys
p = sys.argv[1]
s = open(p).read()
old = $1
new = $2
assert old in s, "mutation target not found"
open(p, "w").write(s.replace(old, new, 1))
PY
}

echo "mutation drills: intent-aware live search"

drill "the live lookup is told which kind was asked for" \
  internal/tools/builtin.go \
  "$(py_sub "'''Intents: livesource.IntentsFor(argStrs(a, \"kinds\")),'''" "''''''")" \
  'TestOpportunitySearchTellsTheLiveLookupWhatKindWasAskedFor|TestATrainingQuestionOutsideTheCorpusComesBackWithACourse' ./internal/tools/

drill "the query asks for the kind of thing wanted" \
  internal/livesource/intent.go \
  "$(py_sub "'''q += \" \" + intentProfiles[in].Term'''" "'''q += \" \" + intentProfiles[IntentWork].Term'''")" \
  'TestBochaAsksForTheRightThingPerIntent|TestBochaReturnsCoursesForATrainingLookup' ./internal/livesource/

drill "a course is not offered to somebody looking for work" \
  internal/livesource/bocha.go \
  "$(py_sub "'''!matchesIntent(text, a.intent)'''" "'''false'''")" \
  'TestBochaDoesNotOfferCoursesToSomebodyLookingForWork' ./internal/livesource/

drill "the warning matches the intent" \
  internal/livesource/bocha.go \
  "$(py_sub "'''Caveat:    intentProfiles[a.intent].Caveat,'''" "'''Caveat:    intentProfiles[IntentWork].Caveat,'''")" \
  'TestBochaWarnsAboutTheFraudThatMatchesTheIntent' ./internal/livesource/

drill "intent words do not satisfy the trade filter" \
  internal/livesource/intent.go \
  "$(py_sub "'''isIntentWord := false'''" "'''isIntentWord := false\n\t\tif true {\n\t\t\tout = append(out, w)\n\t\t\tcontinue\n\t\t}'''")" \
  'TestBochaKeepsTheTradeFilterWhenTheQuerySaysTraining' ./internal/livesource/

drill "a multi-word query does not return nothing" \
  internal/livesource/bocha.go \
  "$(py_sub "'''	for _, n := range needles {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false'''" "'''	for _, n := range needles {
		if !strings.Contains(text, n) {
			return false
		}
	}
	return true'''")" \
  'TestBochaMultiWordQueriesDoNotReturnNothing' ./internal/livesource/

drill "Brave judges a page against the intent too" \
  internal/livesource/websearch.go \
  "$(py_sub "'''!matchesIntent(text, in)'''" "'''false'''")" \
  'TestWebSearchDropsPagesThatDoNotMatchTheIntent' ./internal/livesource/

drill "job maps to work rather than to nothing" \
  internal/livesource/intent.go \
  "$(py_sub "'''	string(domain.KindJob):      IntentWork,'''" "''''''")" \
  'TestIntentsForMapsCorpusKindsOntoSearchableIntents' ./internal/livesource/

drill "the card reads the intent" \
  web/static/app.js \
  "$(py_sub "'''x.intent === \"training\"'''" "'''false'''")" \
  'TestLiveCardTellsACourseFromAnOpening' ./web/

echo
echo "$pass fence(s) held, $fail did not"
[ "$fail" -eq 0 ]

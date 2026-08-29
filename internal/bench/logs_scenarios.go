package bench

// The log-reading track.
//
// Each scenario states the question in the words an agent would actually have
// arrived with, and the skilled baseline's grep pattern is derived from THAT
// question — never from the answer. Where a plausible pattern misses the
// answer, the row records the miss rather than being quietly reworded until
// the baseline succeeds. Rewording until every grep hits would be the easiest
// way to make this suite dishonest.

import "regexp"

// LogScenarios returns the log track. spillRoot is a caller-owned temp
// directory; the scenarios never write anywhere else.
func LogScenarios(spillRoot string) []Scenario {
	return []Scenario{
		{
			Name:     "restart-loop",
			Track:    TrackLogs,
			Question: "A service is flapping. Is it restart-looping, and how far has the restart counter climbed?",
			Corpus:   "SYNTHETIC — 3,616 lines of systemd/kernel boot chatter with a 16-restart loop buried inside it",
			Answer:   regexp.MustCompile(`614`),
			Note: "The case exact-duplicate deduplication cannot touch: no two counter lines are byte-identical, " +
				"so a dedup pass collapses nothing and the loop stays buried. Grouping by shape is what surfaces it.",
			Run: func() ([]Observation, error) {
				corpus := restartLoopLog()
				return LogArms(corpus, `restart`, spillRoot)
			},
		},
		{
			Name:     "build-failure",
			Track:    TrackLogs,
			Question: "The build failed. Which file broke, and what was the error?",
			Corpus:   "SYNTHETIC — 500 uniform compile-progress lines, one compiler diagnostic, one verdict",
			Answer:   regexp.MustCompile(`transport\.go:127`),
			Note: "The baseline greps 'error|FAILED|fail' — the obvious pattern from the question. Go's compiler " +
				"diagnostic contains none of those words, so this row is expected to show a baseline MISS. That " +
				"is the point of keeping it: the pattern was reasonable and it still missed.",
			Run: func() ([]Observation, error) {
				corpus := buildFailureLog()
				return LogArms(corpus, `error|FAILED|fail`, spillRoot)
			},
		},
		{
			Name:             "access-log-500s",
			Track:            TrackLogs,
			Question:         "Something is throwing 500s. Which endpoint is it?",
			Corpus:           "SYNTHETIC — 20,000 access-log lines, two of them 500s on the same path",
			Answer:           regexp.MustCompile(`/api/plan/settle`),
			LightLosesAnswer: true,
			Note: "THE ROW light-tools LOSES, and the most informative one here. All 20,000 lines share a " +
				"shape, so they collapse into a single template group whose variable slots are summarised " +
				"INDEPENDENTLY: the view reports that a 500 occurred and that six distinct paths exist, but " +
				"the correlation between those two slots is gone. It can tell you a 500 happened; it cannot " +
				"tell you which endpoint. A skilled grep answers this outright and in fewer bytes. " +
				"Template collapse summarises what VARIES, so it is strong on repetition and weak on " +
				"correlating two rare values across slots — and this is what that weakness looks like. " +
				"The exact lines remain recoverable through the spill pointer the view carries.",
			Run: func() ([]Observation, error) {
				corpus := accessLog()
				return LogArms(corpus, ` 500 `, spillRoot)
			},
		},
		{
			Name:     "test-failure",
			Track:    TrackLogs,
			Question: "Did the suite pass? If not, which test failed and why?",
			Corpus:   "SYNTHETIC — a verbose Go test run, 360 passes and one failure",
			Answer:   regexp.MustCompile(`TestSpillRecoveryPointerResolves`),
			Run: func() ([]Observation, error) {
				corpus := verboseTestLog()
				return LogArms(corpus, `FAIL`, spillRoot)
			},
		},
		{
			Name:        "short-status",
			Track:       TrackLogs,
			Question:    "Is light-edge running, and since when?",
			Corpus:      "SYNTHETIC — a 13-line systemctl status block",
			Answer:      regexp.MustCompile(`active \(running\)`),
			Adversarial: true,
			Note: "ADVERSARIAL. This sits under the pass-through floor (internal/logs/analyze.go: defaultMinLines 40 " +
				"AND defaultMinBytes 4000), so light-tools must hand it back byte-for-byte and this row must report " +
				"parity. A saving here would mean compaction had started touching output that was already legible. " +
				"The row is a canary, not filler.",
			Run: func() ([]Observation, error) {
				corpus := shortStatusLog()
				return LogArms(corpus, `Active|running`, spillRoot)
			},
		},
	}
}

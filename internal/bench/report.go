package bench

// Rendering docs/BENCHMARK.md.
//
// The report is generated, never hand-edited, for the same reason the README
// samples are (internal/logs/example_test.go): a number written by hand drifts
// away from the code that produced it, and a stale benchmark is worse than
// none because it still looks authoritative.
//
// The renderer's job is not only to print figures. It has to make a row's
// WEAKNESSES as visible as its wins: an adversarial row, a baseline that
// missed the answer, and a repeat-read row that does not generalise all get
// marked in the table itself rather than explained away in prose at the
// bottom that nobody reads.

import (
	"fmt"
	"sort"
	"strings"
)

// ReproduceCommand is the exact invocation that regenerates this report. It
// includes the build tag deliberately: without it the code track measures the
// no-symbol fallback (internal/symbol/extract_stub.go) and a reader comparing
// their output against the published table would conclude it was fabricated.
const ReproduceCommand = "go test -tags treesitter ./internal/bench/ -run TestBenchmarkReport -update"

// Row is one scenario's measured outcome.
type Row struct {
	Scenario     Scenario
	Observations []Observation
}

// baselineMissed reports whether a baseline arm failed to deliver the answer.
func (r Row) baselineMissed(arm string) bool {
	observation, ok := Find(r.Observations, arm)
	if !ok {
		return true
	}
	return !observation.AnswerSurvives(r.Scenario.Answer)
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// Render produces the full markdown document.
func Render(rows []Row) string {
	var b strings.Builder

	b.WriteString("# Benchmark: what reaches the model\n\n")
	b.WriteString("**Generated file — do not edit by hand.** Regenerate with:\n\n")
	b.WriteString("```sh\n" + ReproduceCommand + "\n```\n\n")
	b.WriteString("Every number below is produced by `internal/bench` on a fixed corpus with no ")
	b.WriteString("network, no clock and no randomness, so the same checkout always yields the same table.\n\n")

	writeMethodology(&b)
	writeTrack(&b, "Log reading", TrackLogs, rows)
	writeTrack(&b, "Code reading", TrackCode, rows)
	writeLimitations(&b)

	return b.String()
}

func writeMethodology(b *strings.Builder) {
	b.WriteString("## What is measured\n\n")
	b.WriteString("**Delivered bytes** — how much text has to be put in front of a model to answer a ")
	b.WriteString("fixed question, and **calls** — how many round trips that takes.\n\n")
	b.WriteString("This is *not* a measure of task success, wall time, or answer quality. It cannot ")
	b.WriteString("tell you an agent solved anything faster. It measures context cost and nothing else.\n\n")

	b.WriteString("Bytes, not tokens: no tokenizer is involved. Bytes and tokens are not proportional ")
	b.WriteString("across content types — repetitive log output compresses into relatively fewer tokens ")
	b.WriteString("than prose does, so a byte ratio probably *understates* the log-track difference. ")
	b.WriteString("The conservative direction is the right one for a published number.\n\n")

	b.WriteString("## The three arms\n\n")
	b.WriteString("| Arm | What it does | Calls |\n| --- | --- | --- |\n")
	b.WriteString("| `native-naive` | Returns the whole thing: the entire file, the entire stream. | 1 |\n")
	b.WriteString("| `native-skilled` | Greps first, then reads a bounded window around the hits. | 2+ |\n")
	b.WriteString("| `light-tools` | The real shipped handlers, with the options the tools actually pass. | 1 |\n\n")

	b.WriteString("**Quote the headline against `native-skilled`, not `native-naive`.** ")
	b.WriteString("The naive arm is real behaviour — it is what an agent does on first contact with an ")
	b.WriteString("unfamiliar corpus — but a competent agent does better, and comparing only against the ")
	b.WriteString("weaker baseline would be marketing.\n\n")

	b.WriteString("### The rule that keeps the skilled baseline fair\n\n")
	b.WriteString("A grep is only as good as the pattern you could think of *before* you had the answer. ")
	b.WriteString("So the skilled arm's pattern is derived from the **question**, never from the **answer**. ")
	b.WriteString("Searching a restart log for `restart` is fair; searching it for `614` is not, because ")
	b.WriteString("that is the answer and assuming it turns the baseline into a lookup of a fact it already had.\n\n")
	b.WriteString("Applied honestly, this rule costs light-tools rows. Where a question names the string ")
	b.WriteString("it is looking for, a skilled grep is extremely efficient and wins. Those rows are in the ")
	b.WriteString("table. The conclusion to draw is not *light always delivers less* — it is that grep ")
	b.WriteString("requires you to already know what you are looking for, and an outline does not.\n\n")

	b.WriteString("### Answer preservation\n\n")
	b.WriteString("Delivering fewer bytes is trivial if you may delete the answer. Every scenario carries a ")
	b.WriteString("regexp matching the fact that answers its question, checked against what each arm ")
	b.WriteString("actually delivered:\n\n")
	b.WriteString("- If the **light** arm loses the answer, the test suite **fails**. That claim does not ship.\n")
	b.WriteString("- If a **baseline** arm loses it, the row is marked ✗ and quotes **no ratio** — a baseline ")
	b.WriteString("is never credited for bytes it saved by not answering the question.\n\n")

	b.WriteString("### Reading the marks\n\n")
	b.WriteString("| Mark | Meaning |\n| --- | --- |\n")
	b.WriteString("| † | **Adversarial row.** Chosen because light-tools is *not* expected to win it. |\n")
	b.WriteString("| ✗ | That arm did **not** deliver the answer. No ratio is quoted against it. |\n")
	b.WriteString("| ‡ | **Repeat read.** Measures a second look at unchanged bytes, not first contact. |\n")
	b.WriteString("| **lost** | **light-tools did not answer this row.** A known limitation, kept in the table. |\n\n")
}

func writeTrack(b *strings.Builder, title, track string, rows []Row) {
	selected := make([]Row, 0, len(rows))
	for _, row := range rows {
		if row.Scenario.Track == track {
			selected = append(selected, row)
		}
	}
	if len(selected) == 0 {
		return
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return selected[i].Scenario.Name < selected[j].Scenario.Name
	})

	fmt.Fprintf(b, "## %s\n\n", title)
	b.WriteString("| Scenario | Corpus | naive | skilled | light | light vs skilled | Calls (n/s/l) |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | :---: |\n")

	for _, row := range selected {
		naive, _ := Find(row.Observations, ArmNaive)
		skilled, _ := Find(row.Observations, ArmSkilled)
		light, _ := Find(row.Observations, ArmLight)

		name := "`" + row.Scenario.Name + "`"
		if row.Scenario.Adversarial {
			name += " †"
		}
		if row.Scenario.ContextCarried {
			name += " ‡"
		}

		lightCell := humanBytes(light.Delivered)
		skilledCell := humanBytes(skilled.Delivered)
		comparison := "—"
		switch {
		case row.Scenario.LightLosesAnswer:
			// Never quote a ratio in light-tools' favour on a row it did not
			// answer. Fewer bytes that miss the question is not a win.
			lightCell += " ✗"
			comparison = "**lost**"
		case row.baselineMissed(ArmSkilled):
			skilledCell += " ✗"
			comparison = "n/a"
		case light.Delivered > 0:
			// Two decimals below 1.0: rendering a row where light delivered 25
			// times MORE than the baseline as "0.0×" would round the bad news
			// away, which is the one thing this column must not do.
			value := ratio(skilled.Delivered, light.Delivered)
			if value >= 1 {
				comparison = fmt.Sprintf("%.1f×", value)
			} else {
				comparison = fmt.Sprintf("%.2f× (light %.1f× larger)",
					value, float64(light.Delivered)/float64(skilled.Delivered))
			}
		}

		naiveCell := humanBytes(naive.Delivered)
		if row.baselineMissed(ArmNaive) {
			naiveCell += " ✗"
		}

		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s | %d/%d/%d |\n",
			name, humanBytes(row.Scenario.CorpusBytes), naiveCell, skilledCell,
			lightCell, comparison,
			naive.Calls, skilled.Calls, light.Calls)
	}
	b.WriteString("\n")
	writeTally(b, selected)

	for _, row := range selected {
		fmt.Fprintf(b, "**`%s`** — %s\n\n", row.Scenario.Name, row.Scenario.Question)
		fmt.Fprintf(b, "> %s\n", row.Scenario.Corpus)
		if row.Scenario.Note != "" {
			fmt.Fprintf(b, ">\n> %s\n", row.Scenario.Note)
		}
		if row.Scenario.LightLosesAnswer {
			b.WriteString(">\n> **light-tools does not answer this one.** The compacted view is smaller, ")
			b.WriteString("and smaller is worth nothing here because it does not contain the answer. ")
			b.WriteString("A skilled grep wins this row outright. The exact lines stay recoverable through ")
			b.WriteString("the spill pointer the view carries, but that costs another call and the reader ")
			b.WriteString("has to know to make it.\n")
		}
		if row.baselineMissed(ArmSkilled) {
			b.WriteString(">\n> **The skilled baseline did not find the answer here.** ")
			b.WriteString("Its pattern was a reasonable guess from the question and it still missed, ")
			b.WriteString("which is why no ratio is quoted for this row.\n")
		}
		b.WriteString("\n")
	}
}

// writeTally states the win/loss count in words, immediately under the table.
//
// It exists so nobody has to squint at a ratio column to find out how the tool
// actually did. A reader who takes only one line from a track should take an
// accurate one, including when it is unflattering.
func writeTally(b *strings.Builder, rows []Row) {
	won, lost, unanswered := 0, 0, 0
	for _, row := range rows {
		skilled, _ := Find(row.Observations, ArmSkilled)
		light, _ := Find(row.Observations, ArmLight)
		switch {
		case row.Scenario.LightLosesAnswer:
			unanswered++
		case row.baselineMissed(ArmSkilled):
			won++ // the baseline did not answer; light did
		case light.Delivered < skilled.Delivered:
			won++
		default:
			lost++
		}
	}

	fmt.Fprintf(b, "**Against the skilled baseline: light-tools delivered less in %d of %d rows, "+
		"more in %d, and failed to answer %d.**\n\n", won, len(rows), lost, unanswered)
	b.WriteString("Round trips are counted separately and are not in that tally — several rows where ")
	b.WriteString("light-tools delivers *more* bytes still answer in one call where the baseline needs two.\n\n")
	return
}

func writeLimitations(b *strings.Builder) {
	b.WriteString("## Limitations\n\n")
	b.WriteString("Read these before quoting anything above.\n\n")

	b.WriteString("1. **The corpora are synthetic.** They are generated by committed code ")
	b.WriteString("(`corpus.go`, `code_fixtures.go`) with shapes drawn from real systemd, compiler, ")
	b.WriteString("access-log and test output — not captured from a production host. Real logs carry ")
	b.WriteString("hostnames and paths that do not belong in a public repository, and a multi-megabyte ")
	b.WriteString("capture makes the repo worse for everyone who clones it. **This is the largest ")
	b.WriteString("weakness here:** a generator written by the party who benefits from the result can ")
	b.WriteString("flatter it. The mitigations are partial — the generators are short enough to audit, ")
	b.WriteString("and the suite includes rows the tool loses. Someone who does not trust a synthetic ")
	b.WriteString("corpus should run the same arms over their own logs; they are exported for that.\n\n")

	b.WriteString("2. **The baselines are modelled, not executed.** No real `cat` or `grep` subprocess ")
	b.WriteString("runs. The models are simple and deliberately generous to the baseline.\n\n")

	b.WriteString("3. **The naive arm is uncapped.** It delivers the whole corpus. An agent whose shell ")
	b.WriteString("tool truncates at some limit delivers less than the figure shown — though truncation ")
	b.WriteString("also silently drops whatever fell outside the cut, often the verdict at the end. ")
	b.WriteString("Rather than invent a specific competitor's cap, the corpus size is printed so you can ")
	b.WriteString("apply your own.\n\n")

	b.WriteString("4. **This measures delivered context, not task success.** No model was run. Nothing ")
	b.WriteString("here shows an agent solved a task faster or better — only that less had to be put in ")
	b.WriteString("front of it.\n\n")

	b.WriteString("5. **The code track assumes the symbol is already known.** It measures extraction, not ")
	b.WriteString("search. light-tools is not a code-intelligence layer and is not credited as one.\n\n")

	b.WriteString("6. **Absolute paths are normalised.** `filetool` returns absolute paths, which would ")
	b.WriteString("make byte counts vary by temp directory. The fixture root is replaced with `<root>` ")
	b.WriteString("before counting, identically in every arm.\n\n")

	b.WriteString("7. **No aggregate across tracks is published.** The two tracks measure different ")
	b.WriteString("mechanisms — repetition collapse and targeted extraction — and one blended figure ")
	b.WriteString("would be an average of incomparable things.\n\n")

	b.WriteString("## What this measurement found against us\n\n")
	b.WriteString("Template collapse summarises each variable slot **independently**, so the correlation ")
	b.WriteString("between two slots is lost. When 20,000 access-log lines share one shape, the view ")
	b.WriteString("correctly reports that a `500` occurred and that several distinct paths exist — but ")
	b.WriteString("not *which path* returned the 500. The `access-log-500s` row is marked **lost** for ")
	b.WriteString("exactly this reason, and a skilled `grep` beats it there on both counts.\n\n")
	b.WriteString("The general shape of it: compaction is strong where the signal is **repetition** and ")
	b.WriteString("weak where the signal is **a rare correlation between two fields**. If you already ")
	b.WriteString("know the string you are looking for, grep for it — that is not a concession, it is ")
	b.WriteString("the correct tool. What compaction gives you is the case where you *cannot* name the ")
	b.WriteString("string yet, because you do not know what is in the log.\n\n")
	b.WriteString("This section exists because a benchmark that never reports against its own tool is ")
	b.WriteString("not measuring anything.\n")
}
